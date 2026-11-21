package codexacp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// history is a made-up thread's items, as thread/items/list lists them.
func history() []map[string]any {
	entry := func(item map[string]any) map[string]any { return map[string]any{"turnId": "turn_1", "item": item} }
	return []map[string]any{
		entry(map[string]any{"type": "userMessage", "id": "u_1", "clientId": nil, "content": []any{
			map[string]any{"type": "text", "text": "Fix the\nbug", "text_elements": []any{}},
			map[string]any{"type": "localImage", "path": "/w/proj/shot.png"},
			map[string]any{"type": "image", "url": "https://x/y.png"}}}),
		entry(map[string]any{"type": "reasoning", "id": "rs_1", "summary": []string{"Reading", "Fixing"}, "content": []string{}}),
		entry(map[string]any{"type": "commandExecution", "id": "call_1", "command": "/bin/zsh -lc 'go test'", "cwd": "/w/proj", "processId": nil,
			"source": "unifiedExecStartup", "status": "failed", "commandActions": []any{map[string]any{"type": "unknown", "command": "go test"}},
			"aggregatedOutput": "FAIL\n", "exitCode": 1, "durationMs": 9}),
		entry(map[string]any{"type": "agentMessage", "id": "msg_1", "text": "Fixed.", "phase": nil, "memoryCitation": nil, "delivery": nil, "questions": nil}),
		entry(map[string]any{"type": "contextCompaction", "id": "cc_1"}),
	}
}

// A load resumes the thread with its turns left out and replays its items
// from thread/items/list, up to the last one thread/resume knew: the
// title first (the first prompt's text, the thread having no name), then
// the user's message and the agent's under their item ids, thoughts, and
// tool calls as they ended.
func TestLoadReplays(t *testing.T) {
	f := codex()
	thread := appservertest.Thread("thr_7", "/w/proj")
	opened := appservertest.Opened(thread)
	opened["itemsBackwardsCursor"] = "after_cc_1"
	f.On("thread/resume", appservertest.Reply(opened))
	items := history()
	f.On("thread/items/list", func(params json.RawMessage) (any, error) {
		var p struct {
			Cursor *string `json:"cursor"`
			Sort   string  `json:"sortDirection"`
		}
		_ = json.Unmarshal(params, &p)
		switch {
		case p.Sort == "desc":
			return map[string]any{"data": items[4:], "nextCursor": nil, "backwardsCursor": nil}, nil
		case p.Cursor == nil:
			return map[string]any{"data": items[:2], "nextCursor": "page2", "backwardsCursor": nil}, nil
		}
		late := map[string]any{"turnId": "turn_2", "item": map[string]any{"type": "agentMessage", "id": "msg_late", "text": "late"}}
		return map[string]any{"data": append(append([]map[string]any{}, items[2:]...), late), "nextCursor": nil, "backwardsCursor": nil}, nil
	})
	h := initialized(t, f)
	resp, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: "thr_7", Cwd: "/w/proj", MCPServers: []acp.MCPServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ConfigOptions) != 4 || resp.Modes.CurrentModeID != "agent" {
		t.Errorf("load answered %s", jsonOf(t, resp))
	}
	if got := string(f.Calls("thread/resume")[0]); got != `{"threadId":"thr_7","cwd":"/w/proj","config":{"features":{"cwd_relative_turn_diffs":false},"projects":{"/w/proj":{"trust_level":"trusted"}}},"excludeTurns":true}` {
		t.Errorf("thread/resume %s", got)
	}
	if got := string(f.Calls("thread/items/list")[0]); got != `{"threadId":"thr_7","cursor":"after_cc_1","limit":1,"sortDirection":"desc"}` {
		t.Errorf("first items/list %s", got)
	}
	check(t, h.updates(),
		`{"sessionUpdate":"session_info_update","title":"Fix the bug"}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"Fix the\nbug"},"messageId":"u_1"}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"resource_link","name":"shot.png","uri":"file:///w/proj/shot.png"},"messageId":"u_1"}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"[@image](https://x/y.png)"},"messageId":"u_1"}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Reading"},"messageId":"rs_1"}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Fixing"},"messageId":"rs_1"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"go test","name":"exec_command","kind":"execute","status":"failed",`+
			`"content":[{"type":"terminal","terminalId":"call_1"}],"rawInput":{"command":"go test","cwd":"/w/proj"},"_meta":{"terminal_info":{"cwd":"/w/proj","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"failed","_meta":{"terminal_exit":{"exit_code":1,"signal":null,"terminal_id":"call_1"},"terminal_output_delta":{"data":"FAIL\n","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Fixed."},"messageId":"msg_1"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"cc_1","title":"Compact conversation","kind":"think","status":"completed"}`)
}

// A legacy thread's history is its turns' items; a named thread's title
// is its name. Resume replays nothing.
func TestLoadLegacyAndResume(t *testing.T) {
	f := codex()
	thread := appservertest.Thread("thr_7", "/w/proj")
	thread["historyMode"], thread["name"] = "legacy", "Named"
	f.On("thread/resume", appservertest.Reply(appservertest.Opened(thread)))
	withTurns := appservertest.Thread("thr_7", "/w/proj")
	turn := appservertest.TurnOf("turn_1", "completed")
	turn["items"] = []any{history()[3]["item"]}
	withTurns["turns"] = []any{turn}
	f.On("thread/read", appservertest.Reply(map[string]any{"thread": withTurns, "readState": nil}))
	h := initialized(t, f)
	if _, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: "thr_7", Cwd: "/w/proj", MCPServers: []acp.MCPServer{}}); err != nil {
		t.Fatal(err)
	}
	check(t, h.updates(), `{"sessionUpdate":"session_info_update","title":"Named"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Fixed."},"messageId":"msg_1"}`)
	if got := string(f.Calls("thread/read")[0]); got != `{"threadId":"thr_7","includeTurns":true}` {
		t.Errorf("thread/read %s", got)
	}
	if _, err := h.conn.ResumeSession(t.Context(), &acp.ResumeSessionRequest{SessionID: "thr_7", Cwd: "/w/proj"}); err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(h.updates(), "\n")); n != 2 {
		t.Errorf("resume replayed: %d updates", n)
	}
}

// A thread Codex hasn't written yet opens from the app-server's live
// state, with no history; one it doesn't have at all fails as codex-acp
// fails it (Internal error, Codex's words in data.details, which a
// client reads as a session gone); one another Codex client writes says
// so.
func TestLoadErrors(t *testing.T) {
	f := codex()
	f.On("thread/resume", appservertest.Fail(-32600, "no rollout found for thread id thr_new", nil))
	f.On("thread/read", appservertest.Reply(map[string]any{"thread": appservertest.Thread("thr_new", "/w/proj"), "readState": nil}))
	h := initialized(t, f)
	load := func(id string) error {
		_, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: acp.SessionID(id), Cwd: "/w/proj", MCPServers: []acp.MCPServer{}})
		return err
	}
	if err := load("thr_new"); err != nil {
		t.Errorf("a thread not yet written: %v", err)
	}
	if got := h.updates(); got != "" {
		t.Errorf("replayed %s", got)
	}
	f.On("thread/read", appservertest.Fail(-32600, "thread not found: thr_gone", nil))
	var e *acp.Error
	if err := load("thr_gone"); !errors.As(err, &e) || e.Code != acp.ErrorCodeInternalError || string(e.Data) != `{"details":"no rollout found for thread id thr_new"}` {
		t.Errorf("a thread Codex hasn't: %v (%s)", err, e.Data)
	}
	f.On("thread/resume", appservertest.Fail(-32600, "thread thr_7 already has an active writer", nil))
	if err := load("thr_7"); !errors.As(err, &e) || e.Code != acp.ErrorCodeInvalidRequest || field(t, e.Data, "reason") != `"thread_active_writer"` {
		t.Errorf("a thread another client writes: %v (%s)", err, e.Data)
	}
}

// field is a top-level field of a JSON object, as JSON.
func field(t *testing.T, obj json.RawMessage, name string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		t.Fatalf("%s: %v", obj, err)
	}
	return string(m[name])
}

// A history item that doesn't decode (a field Codex re-typed) is left out
// and logged by its kind, never its content; an item of a kind the agent
// doesn't know shows nothing; the rest of the page loads.
func TestLoadSkipsBadItems(t *testing.T) {
	f := codex()
	thread := appservertest.Thread("thr_7", "/w/proj")
	opened := appservertest.Opened(thread)
	opened["itemsBackwardsCursor"] = "after_msg_1"
	f.On("thread/resume", appservertest.Reply(opened))
	entry := func(item map[string]any) map[string]any { return map[string]any{"turnId": "turn_1", "item": item} }
	items := []map[string]any{
		history()[0],
		entry(map[string]any{"type": "commandExecution", "id": "call_bad", "command": []string{"secret-word"}, "status": "completed"}),
		entry(map[string]any{"type": "somethingNew", "id": "new_1", "shape": map[string]any{"a": 1}}),
		history()[3],
	}
	f.On("thread/items/list", func(params json.RawMessage) (any, error) {
		var p struct {
			Sort string `json:"sortDirection"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Sort == "desc" {
			return map[string]any{"data": items[3:], "nextCursor": nil, "backwardsCursor": nil}, nil
		}
		return map[string]any{"data": items, "nextCursor": nil, "backwardsCursor": nil}, nil
	})
	h := initialized(t, f)
	if _, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: "thr_7", Cwd: "/w/proj", MCPServers: []acp.MCPServer{}}); err != nil {
		t.Fatal(err)
	}
	updates := strings.Split(h.updates(), "\n")
	if len(updates) != 5 || !strings.Contains(updates[4], `"messageId":"msg_1"`) || strings.Contains(h.updates(), "call_bad") {
		t.Errorf("updates\n%s", h.updates())
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "method=thread/items/list") || !strings.Contains(logs, "commandExecution") ||
		strings.Contains(logs, "secret-word") {
		t.Errorf("log\n%s", logs)
	}
}

// A legacy thread's turn with a bad item still replays its other items.
func TestLoadLegacySkipsBadItems(t *testing.T) {
	f := codex()
	thread := appservertest.Thread("thr_7", "/w/proj")
	thread["historyMode"], thread["name"] = "legacy", "Named"
	f.On("thread/resume", appservertest.Reply(appservertest.Opened(thread)))
	withTurns := appservertest.Thread("thr_7", "/w/proj")
	turn := appservertest.TurnOf("turn_1", "completed")
	turn["items"] = []any{map[string]any{"type": "agentMessage", "id": "msg_0", "text": 7}, history()[3]["item"]}
	withTurns["turns"] = []any{turn}
	f.On("thread/read", appservertest.Reply(map[string]any{"thread": withTurns, "readState": nil}))
	h := initialized(t, f)
	if _, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: "thr_7", Cwd: "/w/proj", MCPServers: []acp.MCPServer{}}); err != nil {
		t.Fatal(err)
	}
	check(t, h.updates(), `{"sessionUpdate":"session_info_update","title":"Named"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Fixed."},"messageId":"msg_1"}`)
}

// A session that fails to open once its thread is open lets go of the
// thread: a new session whose models can't be listed, and a load whose
// history can't be read.
func TestFailedOpenUnsubscribes(t *testing.T) {
	f := codex()
	f.On("model/list", appservertest.Fail(-32603, "models unavailable", nil))
	h := initialized(t, f)
	if _, err := h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj", MCPServers: []acp.MCPServer{}}); err == nil {
		t.Fatal("a new session opened without models")
	}
	f.On("model/list", appservertest.Reply(appservertest.Models()))
	opened := appservertest.Opened(appservertest.Thread("thr_7", "/w/proj"))
	opened["itemsBackwardsCursor"] = "after_cc_1"
	f.On("thread/resume", appservertest.Reply(opened))
	f.On("thread/items/list", appservertest.Fail(-32603, "history unavailable", nil))
	if _, err := h.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: "thr_7", Cwd: "/w/proj", MCPServers: []acp.MCPServer{}}); err == nil {
		t.Fatal("a load replayed no history")
	}
	got := f.Calls("thread/unsubscribe")
	if len(got) != 2 || string(got[0]) != `{"threadId":"thr_1"}` || string(got[1]) != `{"threadId":"thr_7"}` {
		t.Errorf("unsubscribe %s", got)
	}
}
