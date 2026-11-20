package codexacp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// A message steered while a turn runs goes into it (turn/steer, the turn
// named); with none running, a client asking for promptRequired is told
// to send it as a prompt, and another gets a turn of its own.
func TestSteering(t *testing.T) {
	f := codex()
	f.On("turn/steer", appservertest.Reply(map[string]any{"turnId": "turn_1"}))
	release := make(chan struct{})
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.Then("turn/start", func(json.RawMessage) {
		<-release
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "completed")})
	})
	h := initialized(t, f)
	sid := h.session()
	steer := func() steering.Outcome {
		t.Helper()
		out, err := steering.Steer(t.Context(), h.conn, sid, []acp.ContentBlock{textBlock("also this")})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := steer(); got != steering.PromptRequired {
		t.Errorf("idle: %s", got)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
		done <- err
	}()
	if _, err := f.Await(t.Context(), "turn/start", 0); err != nil {
		t.Fatal(err)
	}
	if got := steer(); got != steering.Injected {
		t.Errorf("running: %s", got)
	}
	if got := string(f.Calls("turn/steer")[0]); got != `{"threadId":"thr_1","expectedTurnId":"turn_1","input":[{"type":"text","text":"also this","text_elements":[]}]}` {
		t.Errorf("turn/steer %s", got)
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var resp steering.Response
	err := h.conn.Call(t.Context(), steering.Method, steering.Request[acp.ContentBlock]{SessionID: sid, Prompt: []acp.ContentBlock{textBlock("next")}}, &resp)
	if err != nil || resp.Outcome != steering.StartedNewTurn {
		t.Errorf("idle, no idle behaviour asked: %v, %v", resp, err)
	}
	if _, err := f.Await(t.Context(), "turn/start", 1); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
}
