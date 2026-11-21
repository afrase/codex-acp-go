package codexacp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// A subagent's approvals come to the session whose turn spawned it, as
// codex-acp routes them for a client without subagent sessions; so do
// those of a subagent that subagent spawned. Its own items, text and
// turn show nothing, and its turn's end doesn't end the prompt. A thread
// no session knows is still refused.
func TestSubagentApprovals(t *testing.T) {
	f := codex()
	answers := make(chan string, 3)
	ask := func(thread, item string) {
		answer, err := f.Ask(context.Background(), "item/commandExecution/requestApproval", map[string]any{
			"threadId": thread, "turnId": "turn_c", "itemId": item, "command": "/bin/zsh -lc 'make'", "cwd": "/w/proj",
			"commandActions": []any{map[string]any{"type": "unknown", "command": "make"}}})
		if err != nil {
			answers <- err.Error()
		} else {
			answers <- string(answer)
		}
	}
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.Then("turn/start", func(json.RawMessage) {
		f.Notify("turn/started", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "inProgress")})
		f.Notify("item/started", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "item": map[string]any{
			"type": "collabAgentToolCall", "id": "call_spawn", "tool": "spawnAgent", "status": "inProgress",
			"senderThreadId": "thr_1", "receiverThreadIds": []string{"thr_child"}, "prompt": "help", "agentsStates": map[string]any{}}})
		f.Notify("turn/started", map[string]any{"threadId": "thr_child", "turn": appservertest.TurnOf("turn_c", "inProgress")})
		f.Notify("item/started", map[string]any{"threadId": "thr_child", "turnId": "turn_c", "item": map[string]any{
			"type": "commandExecution", "id": "call_c1", "command": "/bin/zsh -lc 'make'", "cwd": "/w/proj", "source": "agent",
			"status": "inProgress", "commandActions": []any{map[string]any{"type": "unknown", "command": "make"}}}})
		f.Notify("item/agentMessage/delta", map[string]any{"threadId": "thr_child", "turnId": "turn_c", "itemId": "msg_c", "delta": "child text"})
		ask("thr_child", "call_c1")
		f.Notify("item/started", map[string]any{"threadId": "thr_child", "turnId": "turn_c", "item": map[string]any{
			"type": "subAgentActivity", "id": "act_1", "kind": "started", "agentThreadId": "thr_grandchild", "agentPath": "/root/helper/worker"}})
		ask("thr_grandchild", "call_g1")
		ask("thr_stranger", "call_s1")
		f.Notify("turn/completed", map[string]any{"threadId": "thr_child", "turn": appservertest.TurnOf("turn_c", "completed")})
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "completed")})
	})
	h := initialized(t, f)
	h.c.permit = pick("allow_once")
	sid := h.session()
	resp, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
	if err != nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("prompt: %v, %v", resp, err)
	}
	for i, want := range []string{`{"decision":"accept"}`, `{"decision":"accept"}`, `{"decision":"cancel"}`} {
		if got := <-answers; got != want {
			t.Errorf("answer %d: %s, want %s", i, got, want)
		}
	}
	for i, item := range []string{"call_c1", "call_g1"} {
		req := h.waitAsked(i + 1)
		if req.SessionID != acp.SessionID(sid) || string(req.ToolCall.ToolCallID) != item {
			t.Errorf("asked %s", jsonOf(t, req))
		}
	}
	h.c.mu.Lock()
	asked := len(h.c.asked)
	h.c.mu.Unlock()
	if asked != 2 {
		t.Errorf("%d permission requests, want 2", asked)
	}
	if got := h.updates(); strings.Contains(got, "child text") || strings.Contains(got, "call_c1") {
		t.Errorf("a subagent's own updates shown:\n%s", got)
	}
}

// Once its session is closed, a subagent's approvals are refused.
func TestSubagentOfClosedSession(t *testing.T) {
	f := codex()
	f.Turn("turn_1", "completed", itemNote("item/completed", map[string]any{
		"type": "collabAgentToolCall", "id": "call_spawn", "tool": "spawnAgent", "status": "completed",
		"senderThreadId": "thr_1", "receiverThreadIds": []string{"thr_child"}, "agentsStates": map[string]any{}}))
	h := initialized(t, f)
	h.c.permit = pick("allow_once")
	sid := h.session()
	h.prompt(sid, "go")
	approval := map[string]any{"threadId": "thr_child", "turnId": "turn_c", "itemId": "call_c1", "reason": nil}
	if got, err := f.Ask(t.Context(), "item/fileChange/requestApproval", approval); err != nil || string(got) != `{"decision":"accept"}` {
		t.Errorf("open session: %s, %v", got, err)
	}
	if _, err := h.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Ask(t.Context(), "item/fileChange/requestApproval", approval); err != nil || string(got) != `{"decision":"cancel"}` {
		t.Errorf("closed session: %s, %v", got, err)
	}
}
