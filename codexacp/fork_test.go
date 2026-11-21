package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// A fork is a new thread in the request's folder; at a message, it ends
// with the turn that has it (the message's id, or its id before a
// ":segment:N"), found in the thread's items. A message not in the
// thread is Invalid params.
func TestFork(t *testing.T) {
	f := codex()
	f.On("thread/fork", appservertest.Reply(appservertest.Opened(appservertest.Thread("thr_9", "/w/moved"))))
	f.On("thread/read", appservertest.Reply(map[string]any{"thread": appservertest.Thread("thr_1", "/w/proj"), "readState": nil}))
	f.On("thread/items/list", appservertest.Reply(map[string]any{"data": []any{
		map[string]any{"turnId": "turn_1", "item": map[string]any{"type": "agentMessage", "id": "msg_1", "text": "a"}},
		map[string]any{"turnId": "turn_2", "item": map[string]any{"type": "agentMessage", "id": "msg_2", "text": "b"}},
	}, "nextCursor": nil, "backwardsCursor": nil}))
	h := initialized(t, f)
	fork := func(meta map[string]any) (*acp.ForkSessionResponse, error) {
		return h.conn.ForkSession(t.Context(), &acp.ForkSessionRequest{SessionID: "thr_1", Cwd: "/w/moved", Meta: meta})
	}
	resp, err := fork(nil)
	if err != nil || resp.SessionID != "thr_9" || len(resp.ConfigOptions) != 4 {
		t.Fatalf("fork: %v, %v", resp, err)
	}
	at := func(id string) map[string]any {
		return map[string]any{"jetbrains": map[string]any{"air": map[string]any{"fork": map[string]any{"version": 1, "messageId": id}}}}
	}
	if _, err := fork(at("msg_1:segment:2")); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls("thread/fork")
	if string(calls[0]) != `{"threadId":"thr_1","cwd":"/w/moved","config":{"features":{"cwd_relative_turn_diffs":false},"projects":{"/w/moved":{"trust_level":"trusted"}}},"excludeTurns":true}` ||
		field(t, calls[1], "lastTurnId") != `"turn_1"` {
		t.Errorf("thread/fork %s", calls)
	}
	var e *acp.Error
	if _, err := fork(at("msg_x")); !errors.As(err, &e) || e.Code != acp.ErrorCodeInvalidParams {
		t.Errorf("fork at a message not there: %v", err)
	}
	if len(f.Calls("thread/fork")) != 2 {
		t.Error("forked at a message not there")
	}
}

// Delete archives the thread; one Codex doesn't know is deleted already,
// any other failure is the client's.
func TestDelete(t *testing.T) {
	f := codex()
	f.On("thread/archive", appservertest.Reply(map[string]any{}))
	h := initialized(t, f)
	del := func() error {
		_, err := h.conn.DeleteSession(t.Context(), &acp.DeleteSessionRequest{SessionID: "thr_1"})
		return err
	}
	if err := del(); err != nil || string(f.Calls("thread/archive")[0]) != `{"threadId":"thr_1"}` {
		t.Errorf("delete: %v %s", err, f.Calls("thread/archive"))
	}
	f.On("thread/archive", appservertest.Fail(-32600, "no rollout found for thread id thr_1", nil))
	if err := del(); err != nil {
		t.Errorf("delete of a thread gone: %v", err)
	}
	f.On("thread/archive", appservertest.Fail(-32603, "disk full", nil))
	if err := del(); err == nil {
		t.Error("a failed archive deleted")
	}
}

// A list is a page of Codex's threads (unarchived, Codex's default) with no subagents',
// those of the folder asked for, each titled by its name or preview.
func TestList(t *testing.T) {
	f := codex()
	named := appservertest.Thread("thr_1", "/w/proj")
	named["name"], named["updatedAt"] = "Named\nthread", 1_700_000_123
	other := appservertest.Thread("thr_2", "/w/other")
	previewed := appservertest.Thread("thr_3", "/w/proj/")
	previewed["preview"] = "  first  prompt "
	f.On("thread/list", appservertest.Reply(map[string]any{"data": []any{named, other, previewed}, "nextCursor": "next", "backwardsCursor": nil}))
	h := initialized(t, f)
	resp, err := h.conn.ListSessions(t.Context(), &acp.ListSessionsRequest{Cwd: "/w/proj", Cursor: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, resp); got != `{"sessions":[{"sessionId":"thr_1","cwd":"/w/proj","title":"Named thread","updatedAt":"2023-11-14T22:15:23.000Z"},`+
		`{"sessionId":"thr_3","cwd":"/w/proj/","title":"first prompt","updatedAt":"2023-11-14T22:13:20.000Z"}],"nextCursor":"next"}` {
		t.Errorf("list %s", got)
	}
	if got := string(f.Calls("thread/list")[0]); got != `{"cursor":"c1","sourceKinds":["cli","vscode","exec","appServer","unknown"],"useStateDbOnly":true}` {
		t.Errorf("thread/list %s", got)
	}
}

// Deleting an open session closes it first: its turn is interrupted, its
// prompt ends cancelled, and its thread is unsubscribed, then archived.
func TestDeleteOpenSession(t *testing.T) {
	f := codex()
	f.Turn("turn_1", "", appservertest.Note{Method: "item/agentMessage/delta",
		Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "msg_1", "delta": "Hi"}})
	f.On("turn/interrupt", appservertest.Reply(map[string]any{}))
	f.Then("turn/interrupt", func(json.RawMessage) {
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "interrupted")})
	})
	f.On("thread/archive", appservertest.Reply(map[string]any{}))
	h := initialized(t, f)
	sid := h.session()
	done := make(chan *acp.PromptResponse, 1)
	go func() {
		resp, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	h.waitUpdates(1) // the turn runs
	if _, err := h.conn.DeleteSession(t.Context(), &acp.DeleteSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	select {
	case resp := <-done:
		if resp == nil || resp.StopReason != acp.StopReasonCancelled {
			t.Errorf("prompt %v", resp)
		}
	case <-time.After(5 * time.Second): // bounds a hang, orders nothing
		t.Fatal("the deleted session's prompt didn't end")
	}
	var order []string
	for _, c := range f.All() {
		if c.Method == "turn/interrupt" || c.Method == "thread/unsubscribe" || c.Method == "thread/archive" {
			order = append(order, c.Method)
		}
	}
	if got := strings.Join(order, " "); got != "turn/interrupt thread/unsubscribe thread/archive" {
		t.Errorf("calls %s", got)
	}
	if _, err := h.conn.SetSessionMode(t.Context(), &acp.SetSessionModeRequest{SessionID: acp.SessionID(sid), ModeID: "read-only"}); err == nil {
		t.Error("a deleted session is still open")
	}
}
