package codexacp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// ExtMethod answers _session/steering; any other is Method not found.
func (a *agent) ExtMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != steering.Method {
		return a.UnimplementedAgent.ExtMethod(ctx, method, params)
	}
	var req steering.Request[acp.ContentBlock]
	if json.Unmarshal(params, &req) != nil {
		return nil, invalidParams("steering: params")
	}
	return a.steer(ctx, &req)
}

// steer adds a message to the session's running turn (turn/steer), as
// codex-acp does. With no turn running, or one that ended under it, a
// client that asks for promptRequired gets that answer and sends the
// message as a prompt itself; for any other client the message starts a
// turn of its own, as codex-acp starts one for every client.
func (a *agent) steer(ctx context.Context, req *steering.Request[acp.ContentBlock]) (*steering.Response, error) {
	s, err := a.session(acp.SessionID(req.SessionID))
	if err != nil {
		return nil, err
	}
	input := promptInput(req.Prompt)
	if len(input) == 0 {
		return nil, invalidParams("steering: an empty prompt")
	}
	s.mu.Lock()
	m, listed := s.settings.current()
	s.mu.Unlock()
	if hasImage(input) && listed && !takesImages(m) {
		return nil, &acp.Error{Code: acp.ErrorCodeInvalidRequest, Message: "The current model does not support image input"}
	}
	if t := s.running(); t != nil {
		select { // a turn starting: its id first, as codex-acp waits for it
		case <-t.known:
		case <-t.ctx.Done():
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		t.mu.Lock()
		id := t.id
		t.mu.Unlock()
		if id != "" {
			err := a.call(ctx, "turn/steer", appserver.TurnSteerParams{ThreadID: s.id, ExpectedTurnID: id, Input: input}, nil)
			if err == nil {
				return &steering.Response{Outcome: steering.Injected}, nil
			}
			if t := s.running(); t != nil && t.idIs(id) && !strings.Contains(errorDetails(err), "no active turn") {
				return nil, err
			}
		}
	}
	if req.PromptRequired() {
		return &steering.Response{Outcome: steering.PromptRequired, Reason: "noRunningTurn"}, nil
	}
	seq := sequence(ctx)
	go func() { // a turn of its own, whose updates the client gets as a prompt's
		_, err := a.prompt(a.ctx, seq, &acp.PromptRequest{SessionID: acp.SessionID(s.id), Prompt: req.Prompt})
		if err != nil {
			a.log.Warn("steered turn failed", "session", s.id, "err", err)
		}
	}()
	return &steering.Response{Outcome: steering.StartedNewTurn}, nil
}

// idIs says the turn's id is id.
func (t *turn) idIs(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id == id
}
