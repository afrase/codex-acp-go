package codexacp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// session is an open ACP session: a Codex thread this connection has
// started, resumed or forked.
type session struct {
	a   *agent
	id  string // the thread's id
	cwd string

	mu       sync.Mutex
	settings settings
	turn     *turn // the prompt running, if any
	// prompting is held by a prompt from its start to its end: a second
	// prompt waits for the first, as Codex runs one turn at a time.
	prompting chan struct{}
	// cancelSeq is where the latest session/cancel came among the
	// client's messages (acp.SequenceFromContext): a prompt from before it
	// that hasn't started its turn is cancelled.
	cancelSeq uint64
	closed    bool                  // closed, or opened again: no prompt starts a turn on it
	wake      chan struct{}         // closed and replaced at a cancel or the close: the prompts waiting look again
	total     *appserver.TokenCount // the thread's tokens, as last reported
	titled    bool                  // the session has a title: Codex's name, or a prompt's
	asks      int                   // MCP questions asked with no call of their own, for their calls' ids
	published string                // the commands sent last, as JSON
	stale     map[string]bool       // the turns interrupted, by id
	tools     tools
}

func newSession(a *agent, id, cwd string) *session {
	return &session{a: a, id: id, cwd: cwd, prompting: make(chan struct{}, 1), wake: make(chan struct{}), settings: settings{mode: defaultMode, collab: "default"}}
}

// update sends the client one of the session's updates.
func (s *session) update(u acp.SessionUpdate) { s.a.update(s.id, u) }

// open registers the session once its thread is open, with the models
// Codex offers and the thread's own model and effort.
func (a *agent) open(ctx context.Context, s *session, opened appserver.ThreadOpened) error {
	models, err := a.models(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return &acp.Error{Code: acp.ErrorCodeInternalError, Message: "Codex did not return any models"}
	}
	s.settings.models = models
	s.settings.pick(opened.Model, opened.ReasoningEffort)
	if c := opened.CollaborationMode; c != nil && c.Mode != "" {
		s.settings.collab = c.Mode
	}
	a.mu.Lock()
	old := a.sessions[s.id]
	a.sessions[s.id] = s
	a.mu.Unlock()
	if old != nil { // loaded again: the new session takes the thread's notifications
		old.end(errors.New("session opened again"))
	}
	return nil
}

// models is every page of model/list.
func (a *agent) models(ctx context.Context) ([]appserver.Model, error) {
	var out []appserver.Model
	var cursor *string
	for range 100 { // a server that pages forever ends somewhere
		var page appserver.ModelListResponse
		if err := a.call(ctx, "model/list", appserver.ModelListParams{Cursor: cursor}, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// signedIn fails with Authentication required when Codex has no login
// its provider needs, as codex-acp does before opening a thread.
func (a *agent) signedIn(ctx context.Context) error {
	var acct appserver.AccountReadResponse
	if err := a.call(ctx, "account/read", appserver.AccountReadParams{}, &acct); err != nil {
		return err
	}
	if !acct.SignedIn() {
		return &acp.Error{Code: acp.ErrorCodeAuthenticationRequired, Message: "Authentication required"}
	}
	return nil
}

func (a *agent) NewSession(ctx context.Context, req *acp.NewSessionRequest) (*acp.NewSessionResponse, error) {
	if err := a.signedIn(ctx); err != nil {
		return nil, err
	}
	config, err := threadConfig(req.Cwd, req.MCPServers)
	if err != nil {
		return nil, err
	}
	var opened appserver.ThreadOpened
	if err := a.call(ctx, "thread/start", appserver.ThreadStartParams{Cwd: req.Cwd, Config: config}, &opened); err != nil {
		return nil, err
	}
	s := newSession(a, opened.Thread.ID, req.Cwd)
	s.total = &appserver.TokenCount{} // a new thread has used nothing
	if err := a.open(ctx, s, opened); err != nil {
		a.letGo(ctx, s.id)
		return nil, err
	}
	a.out.after(s.id, func() { s.commands(a.ctx, false) })
	s.mu.Lock()
	defer s.mu.Unlock()
	return &acp.NewSessionResponse{SessionID: acp.SessionID(s.id), Modes: s.settings.modeState(), ConfigOptions: s.settings.options()}, nil
}

// CloseSession closes a session; one that isn't open is closed already.
func (a *agent) CloseSession(ctx context.Context, req *acp.CloseSessionRequest) (*acp.CloseSessionResponse, error) {
	s, err := a.session(req.SessionID)
	if err != nil {
		return &acp.CloseSessionResponse{}, nil //nolint:nilerr // a session not open is closed already
	}
	if err := a.closeSession(ctx, s); err != nil {
		return nil, err
	}
	return &acp.CloseSessionResponse{}, nil
}

// closeSession closes s: its turn interrupted first, then its thread
// unsubscribed.
func (a *agent) closeSession(ctx context.Context, s *session) error {
	a.mu.Lock()
	delete(a.sessions, s.id)
	a.forgetChildren(s.id)
	a.mu.Unlock()
	s.mu.Lock()
	s.closeLocked() // a prompt waiting starts no turn
	s.mu.Unlock()
	// A turn still running is interrupted first: once unsubscribed, the
	// thread would go on working with no one to hear it.
	if t := s.running(); t != nil {
		s.interrupt(t)
		select { // a turn whose id isn't known yet is interrupted once it is
		case <-t.known:
			s.interrupt(t)
		case <-t.done:
		case <-ctx.Done():
		case <-a.ctx.Done():
		}
		t.interruptSent(ctx)
	}
	s.end(fmt.Errorf("session %s closed", s.id))
	return a.call(ctx, "thread/unsubscribe", appserver.ThreadID{ThreadID: s.id}, nil)
}

func (a *agent) SetSessionMode(_ context.Context, req *acp.SetSessionModeRequest) (*acp.SetSessionModeResponse, error) {
	s, err := a.session(req.SessionID)
	if err != nil {
		return nil, err
	}
	m, ok := modeByID(string(req.ModeID))
	if !ok {
		return nil, invalidParams("no mode %q", req.ModeID)
	}
	s.mu.Lock()
	s.settings.mode = m.id
	opts := s.settings.options()
	s.mu.Unlock()
	s.update(acp.SessionUpdate{SessionUpdate: "config_option_update", ConfigOptionUpdate: &acp.ConfigOptionUpdate{ConfigOptions: opts}})
	return &acp.SetSessionModeResponse{}, nil
}

func (a *agent) SetSessionConfigOption(ctx context.Context, req *acp.SetSessionConfigOptionRequest) (*acp.SetSessionConfigOptionResponse, error) {
	s, err := a.session(req.SessionID)
	if err != nil {
		return nil, err
	}
	if req.ValueID == nil {
		return nil, invalidParams("option %q takes a value", req.ConfigID)
	}
	value := string(req.ValueID.Value)
	s.mu.Lock()
	st := s.settings
	s.mu.Unlock()
	// apply makes the change on the settings as they are once it is made:
	// another change may have come while Codex was asked.
	var apply func(*settings)
	switch string(req.ConfigID) {
	case optMode:
		m, ok := modeByID(value)
		if !ok {
			return nil, invalidParams("no mode %q", value)
		}
		apply = func(cur *settings) { cur.mode = m.id }
	case optModel:
		st.model = value
		m, listed := st.current()
		if !listed {
			return nil, invalidParams("no model %q", value)
		}
		apply = func(cur *settings) {
			cur.model = value
			if !supports(m, cur.effort) {
				cur.effort = m.DefaultReasoningEffort
			}
		}
	case optEffort:
		m, _ := st.current()
		if !supports(m, value) {
			return nil, invalidParams("model %q has no effort %q", st.model, value)
		}
		apply = func(cur *settings) { cur.effort = value }
	case optCollab:
		if value != "default" && value != "plan" {
			return nil, invalidParams("no collaboration mode %q", value)
		}
		if err := s.setCollab(ctx, value, st); err != nil {
			return nil, err
		}
		apply = func(cur *settings) { cur.collab = value }
	default:
		return nil, invalidParams("no option %q", req.ConfigID)
	}
	s.mu.Lock()
	apply(&s.settings)
	opts := s.settings.options()
	s.mu.Unlock()
	return &acp.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}

// setCollab sets the thread's collaboration mode, with the model and
// effort it runs (experimental thread/settings/update).
func (s *session) setCollab(ctx context.Context, value string, st settings) error {
	effort := st.effort
	return s.a.call(ctx, "thread/settings/update", map[string]any{"threadId": s.id,
		"collaborationMode": appserver.CollaborationMode{Mode: value, Settings: appserver.CollaborationModeSettings{Model: st.model, ReasoningEffort: &effort}}}, nil)
}

// supports says model m takes effort.
func supports(m appserver.Model, effort string) bool {
	for _, e := range m.SupportedReasoningEfforts {
		if e.ReasoningEffort == effort {
			return true
		}
	}
	return false
}

var whitespace = regexp.MustCompile(`\s`)

// threadConfig is the config a thread opens with, as codex-acp's: its
// folder trusted (so the folder's .codex/config.toml loads), turn diffs by
// the repository's root, and the client's MCP servers by name.
func threadConfig(cwd string, servers []acp.MCPServer) (map[string]any, error) {
	config := map[string]any{
		"features": map[string]any{"cwd_relative_turn_diffs": false},
		"projects": map[string]any{cwd: map[string]any{"trust_level": "trusted"}},
	}
	mcp := map[string]any{}
	for _, srv := range servers {
		switch {
		case srv.Stdio != nil:
			env := map[string]string{}
			for _, e := range srv.Stdio.Env {
				env[e.Name] = e.Value
			}
			args := srv.Stdio.Args
			if args == nil {
				args = []string{}
			}
			mcp[whitespace.ReplaceAllString(srv.Stdio.Name, "_")] = map[string]any{"command": srv.Stdio.Command, "args": args, "env": env}
		case srv.HTTP != nil:
			headers := map[string]string{}
			for _, h := range srv.HTTP.Headers {
				headers[h.Name] = h.Value
			}
			mcp[whitespace.ReplaceAllString(srv.HTTP.Name, "_")] = map[string]any{"url": srv.HTTP.URL, "http_headers": headers}
		case srv.SSE != nil:
			return nil, &acp.Error{Code: acp.ErrorCodeInvalidRequest, Message: "Codex doesn't support MCP SSE transport protocol"}
		default:
			return nil, &acp.Error{Code: acp.ErrorCodeInvalidRequest, Message: fmt.Sprintf("Codex doesn't support MCP %s transport protocol", srv.Type)}
		}
	}
	if len(mcp) > 0 {
		config["mcp_servers"] = mcp
	}
	return config, nil
}

func invalidParams(format string, args ...any) error {
	return &acp.Error{Code: acp.ErrorCodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}
