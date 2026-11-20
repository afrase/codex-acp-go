package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// historyPage is how many items a page of a thread's history holds, as
// codex-acp reads them.
const historyPage = 100

// LoadSession resumes the thread and replays its history: the title
// first, then each item, as codex-acp does.
func (a *agent) LoadSession(ctx context.Context, req *acp.LoadSessionRequest) (*acp.LoadSessionResponse, error) {
	s, opened, materialized, err := a.reopen(ctx, string(req.SessionID), req.Cwd, req.MCPServers)
	if err != nil {
		return nil, err
	}
	if err := s.replayHistory(ctx, opened, materialized); err != nil {
		a.forget(s)
		return nil, err
	}
	s.commands(ctx, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return &acp.LoadSessionResponse{Modes: s.settings.modeState(), ConfigOptions: s.settings.options()}, nil
}

// ResumeSession resumes the thread without replaying it.
func (a *agent) ResumeSession(ctx context.Context, req *acp.ResumeSessionRequest) (*acp.ResumeSessionResponse, error) {
	s, _, _, err := a.reopen(ctx, string(req.SessionID), req.Cwd, req.MCPServers)
	if err != nil {
		return nil, err
	}
	s.commands(ctx, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return &acp.ResumeSessionResponse{Modes: s.settings.modeState(), ConfigOptions: s.settings.options()}, nil
}

// reopen resumes a thread as a session (thread/resume, its turns left
// out). A thread Codex hasn't written yet (no rollout before its first
// prompt) is still live in the app-server, so thread/read answers for
// it: it opens with no history (materialized false). A thread another
// Codex client writes is refused, saying so.
func (a *agent) reopen(ctx context.Context, id, cwd string, servers []acp.MCPServer) (*session, appserver.ThreadOpened, bool, error) {
	var opened appserver.ThreadOpened
	if err := a.signedIn(ctx); err != nil {
		return nil, opened, false, err
	}
	config, err := threadConfig(cwd, servers)
	if err != nil {
		return nil, opened, false, err
	}
	materialized := true
	err = a.call(ctx, "thread/resume", appserver.ThreadResumeParams{ThreadID: id, Cwd: cwd, Config: config, ExcludeTurns: true}, &opened)
	switch details := errorDetails(err); {
	case err == nil:
	case activeWriter.MatchString(details):
		return nil, opened, false, &acp.Error{Code: acp.ErrorCodeInvalidRequest,
			Message: "This Codex session is in use by another Codex client (the Codex app, the CLI or an IDE extension). Close the session there or quit that client, then try again.",
			Data:    raw(map[string]any{"reason": "thread_active_writer", "threadId": id, "details": details})}
	case strings.Contains(details, "no rollout found for thread id"):
		var read appserver.ThreadReadResponse
		if a.call(ctx, "thread/read", appserver.ThreadReadParams{ThreadID: id}, &read) != nil {
			return nil, opened, false, err
		}
		opened = appserver.ThreadOpened{Thread: read.Thread, ModelProvider: read.Thread.ModelProvider, ReasoningEffort: read.Thread.ReasoningEffort}
		opened.Model = deref(read.Thread.Model)
		materialized = false
	default:
		return nil, opened, false, err
	}
	s := newSession(a, id, cwd)
	if err := a.open(ctx, s, opened); err != nil {
		return nil, opened, false, err
	}
	return s, opened, materialized, nil
}

var activeWriter = regexp.MustCompile(`\bthread \S+ already has an active writer\b`)

// errorDetails is the text of an app-server's error, as a.call passes it
// on, or "".
func errorDetails(err error) string {
	e, ok := errors.AsType[*acp.Error](err)
	if !ok {
		return ""
	}
	var d struct {
		Details string `json:"details"`
	}
	if json.Unmarshal(e.Data, &d) == nil && d.Details != "" {
		return d.Details
	}
	return e.Message
}

// forget lets go of a session whose opening failed.
func (a *agent) forget(s *session) {
	a.mu.Lock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
	a.mu.Unlock()
}

// replayHistory sends the thread's title and its items: a paginated
// thread's from thread/items/list up to the last one thread/resume knew,
// a legacy thread's from its turns.
func (s *session) replayHistory(ctx context.Context, opened appserver.ThreadOpened, materialized bool) error {
	var items []appserver.Item
	switch {
	case !materialized:
	case opened.Thread.HistoryMode == "legacy":
		var read appserver.ThreadReadResponse
		if err := s.a.call(ctx, "thread/read", appserver.ThreadReadParams{ThreadID: s.id, IncludeTurns: true}, &read); err != nil {
			return err
		}
		for _, t := range read.Thread.Turns {
			for _, b := range t.Items {
				if it, ok := s.item("thread/read", b); ok {
					items = append(items, it)
				}
			}
		}
	case opened.ItemsBackwardsCursor != nil:
		var err error
		if items, err = s.items(ctx, opened.ItemsBackwardsCursor); err != nil {
			return err
		}
	}
	s.historyTitle(opened.Thread, items)
	for _, it := range items {
		for _, u := range replay(it) {
			s.update(u)
		}
	}
	return nil
}

// items is a paginated thread's items, oldest first, up to the item at
// last (thread/resume's itemsBackwardsCursor), so one that comes during
// the read waits for the live session. An item that doesn't decode is
// left out, and the rest load.
func (s *session) items(ctx context.Context, last *string) ([]appserver.Item, error) {
	var end appserver.ThreadItemsListResponse
	if err := s.a.call(ctx, "thread/items/list", appserver.ThreadItemsListParams{ThreadID: s.id, Cursor: last, Limit: 1, SortDirection: "desc"}, &end); err != nil {
		return nil, err
	}
	if len(end.Data) == 0 {
		return nil, nil
	}
	newest, _ := appserver.DecodeItem(end.Data[0].Item) // its id is read even when the rest isn't
	lastID := newest.ID
	var out []appserver.Item
	var cursor *string
	seen := map[string]bool{}
	for {
		var page appserver.ThreadItemsListResponse
		if err := s.a.call(ctx, "thread/items/list", appserver.ThreadItemsListParams{ThreadID: s.id, Cursor: cursor, Limit: historyPage, SortDirection: "asc"}, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Data {
			it, ok := s.item("thread/items/list", e.Item)
			if ok {
				out = append(out, it)
			}
			if it.ID == lastID {
				return out, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" || seen[*page.NextCursor] {
			return out, nil
		}
		seen[*page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// historyTitle sends a loaded thread's title: its name, else its first
// prompt's text, else Codex's preview.
func (s *session) historyTitle(t appserver.Thread, items []appserver.Item) {
	name := title(deref(t.Name))
	if name == "" {
		for _, it := range items {
			if it.Type != "userMessage" {
				continue
			}
			var texts []string
			for _, in := range it.Inputs {
				if in.Type == "text" {
					texts = append(texts, in.Text)
				}
			}
			if name = title(strings.Join(texts, " ")); name != "" {
				break
			}
		}
	}
	if name == "" {
		name = title(t.Preview)
	}
	if name == "" {
		return
	}
	s.mu.Lock()
	s.titled = true
	s.mu.Unlock()
	s.update(titleUpdate(name))
}

func titleUpdate(name string) acp.SessionUpdate {
	return acp.SessionUpdate{SessionUpdate: "session_info_update", SessionInfoUpdate: &acp.SessionInfoUpdate{Title: acp.Nullable[string]{Value: &name, Present: true}}}
}
