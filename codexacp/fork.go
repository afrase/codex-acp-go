package codexacp

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// ForkSession forks the thread into a new session in the request's
// folder (so a move needs no copy), all of it or, with
// _meta.jetbrains.air.fork {version: 1, messageId}, up to the end of the
// turn that has the message, as codex-acp forks for any client. The fork
// replays nothing.
func (a *agent) ForkSession(ctx context.Context, req *acp.ForkSessionRequest) (*acp.ForkSessionResponse, error) {
	last, err := a.forkTurn(ctx, string(req.SessionID), req.Meta)
	if err != nil {
		return nil, err
	}
	config, err := threadConfig(req.Cwd, req.MCPServers)
	if err != nil {
		return nil, err
	}
	var opened appserver.ThreadOpened
	err = a.call(ctx, "thread/fork", appserver.ThreadForkParams{ThreadID: string(req.SessionID), LastTurnID: last, Cwd: req.Cwd, Config: config, ExcludeTurns: true}, &opened)
	if err != nil {
		return nil, err
	}
	s := newSession(a, opened.Thread.ID, req.Cwd)
	if err := a.open(ctx, s, opened); err != nil {
		_ = a.call(ctx, "thread/unsubscribe", appserver.ThreadID{ThreadID: opened.Thread.ID}, nil) // let go of the fork that failed
		return nil, err
	}
	a.out.after(s.id, func() { s.commands(a.ctx, false) })
	s.mu.Lock()
	defer s.mu.Unlock()
	return &acp.ForkSessionResponse{SessionID: acp.SessionID(s.id), Modes: s.settings.modeState(), ConfigOptions: s.settings.options()}, nil
}

var segment = regexp.MustCompile(`:segment:\d+$`)

// forkTurn is the turn a fork ends with, from the fork point in meta: the
// turn of the thread that has the message (its id, or its id without a
// ":segment:N" suffix); "" for no fork point.
func (a *agent) forkTurn(ctx context.Context, thread string, meta map[string]any) (string, error) {
	jb, _ := meta["jetbrains"].(map[string]any)
	air, _ := jb["air"].(map[string]any)
	fork, _ := air["fork"].(map[string]any)
	if fork == nil || fork["version"] != 1.0 && fork["version"] != 1 {
		return "", nil
	}
	id, _ := fork["messageId"].(string)
	if id = strings.TrimSpace(id); id == "" {
		return "", invalidParams("AIR fork messageId must be a non-empty string")
	}
	turns, err := a.turnsOf(ctx, thread)
	if err != nil {
		return "", err
	}
	for _, candidate := range []string{id, segment.ReplaceAllString(id, "")} {
		if t, ok := turns[candidate]; ok {
			return t, nil
		}
	}
	return "", &acp.Error{Code: acp.ErrorCodeInvalidParams, Message: "Fork point message " + id + " was not found in session " + thread,
		Data: raw(map[string]any{"messageId": id})}
}

// turnsOf is the turn of each of the thread's items, by item id.
func (a *agent) turnsOf(ctx context.Context, thread string) (map[string]string, error) {
	out := map[string]string{}
	var read appserver.ThreadReadResponse
	if err := a.call(ctx, "thread/read", appserver.ThreadReadParams{ThreadID: thread}, &read); err != nil {
		return nil, err
	}
	if read.Thread.HistoryMode == "legacy" {
		if err := a.call(ctx, "thread/read", appserver.ThreadReadParams{ThreadID: thread, IncludeTurns: true}, &read); err != nil {
			return nil, err
		}
		for _, t := range read.Thread.Turns {
			for _, b := range t.Items {
				it, _ := appserver.DecodeItem(b) // its id is read even when the rest isn't
				out[it.ID] = t.ID
			}
		}
		return out, nil
	}
	var cursor *string
	seen := map[string]bool{}
	for {
		var page appserver.ThreadItemsListResponse
		if err := a.call(ctx, "thread/items/list", appserver.ThreadItemsListParams{ThreadID: thread, Cursor: cursor, Limit: historyPage, SortDirection: "asc"}, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Data {
			it, _ := appserver.DecodeItem(e.Item)
			out[it.ID] = e.TurnID
		}
		if page.NextCursor == nil || *page.NextCursor == "" || seen[*page.NextCursor] {
			return out, nil
		}
		seen[*page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// DeleteSession archives the thread, as codex-acp's delete does (`codex
// resume` offers to unarchive it); a thread Codex doesn't know is deleted
// already.
func (a *agent) DeleteSession(ctx context.Context, req *acp.DeleteSessionRequest) (*acp.DeleteSessionResponse, error) {
	err := a.call(ctx, "thread/archive", appserver.ThreadID{ThreadID: string(req.SessionID)}, nil)
	if d := errorDetails(err); err != nil && !unknownThread(d) {
		return nil, err
	}
	return &acp.DeleteSessionResponse{}, nil
}

// unknownThread says an app-server error is about a thread it doesn't
// have.
func unknownThread(details string) bool {
	for _, s := range []string{"no rollout found for thread id", "thread not loaded:", "invalid thread id:", "invalid session id:"} {
		if strings.Contains(details, s) {
			return true
		}
	}
	return false
}

// listSources are the threads' sources a list shows: no subagents'.
var listSources = []string{"cli", "vscode", "exec", "appServer", "unknown"}

// ListSessions lists a page of Codex's threads, unarchived, from its
// state database; with a cwd, only that folder's of the page, as
// codex-acp filters them (the same path; a link isn't resolved).
func (a *agent) ListSessions(ctx context.Context, req *acp.ListSessionsRequest) (*acp.ListSessionsResponse, error) {
	if err := a.signedIn(ctx); err != nil {
		return nil, err
	}
	params := appserver.ThreadListParams{SourceKinds: listSources, UseStateDBOnly: true}
	if req.Cursor != "" {
		params.Cursor = &req.Cursor
	}
	var page appserver.ThreadListResponse
	if err := a.call(ctx, "thread/list", params, &page); err != nil {
		return nil, err
	}
	out := &acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}, NextCursor: deref(page.NextCursor)}
	cwd := strings.TrimSpace(req.Cwd)
	for _, t := range page.Data {
		if cwd != "" && filepath.Clean(t.Cwd) != filepath.Clean(cwd) {
			continue
		}
		name := collapse(deref(t.Name))
		if name == "" {
			name = collapse(t.Preview)
		}
		out.Sessions = append(out.Sessions, acp.SessionInfo{SessionID: acp.SessionID(t.ID), Cwd: t.Cwd, Title: name,
			UpdatedAt: time.Unix(t.UpdatedAt, 0).UTC().Format("2006-01-02T15:04:05.000Z")})
	}
	return out, nil
}
