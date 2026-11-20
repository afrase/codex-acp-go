package codexacp

import (
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"
)

// runTurn has the fake's turn send notes, prompts, and gives the updates.
func runTurn(t *testing.T, notes ...note) string {
	t.Helper()
	f := codex()
	f.Turn("turn_1", "completed", notes...)
	h := initialized(t, f)
	sid := h.session()
	if _, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}}); err != nil {
		t.Fatal(err)
	}
	got, ok := strings.CutSuffix(h.updates(), "\n"+`{"sessionUpdate":"session_info_update","title":"go"}`)
	if !ok {
		t.Errorf("no title after the turn:\n%s", got)
	}
	return got
}

func command(status string, actions []map[string]any, output, exit any) map[string]any {
	return map[string]any{"type": "commandExecution", "id": "call_1", "command": "/bin/zsh -lc 'echo hi'", "cwd": "/w/proj", "processId": nil,
		"source": "unifiedExecStartup", "status": status, "commandActions": actions, "aggregatedOutput": output, "exitCode": exit, "durationMs": nil}
}

func check(t *testing.T, got string, want ...string) {
	t.Helper()
	if w := strings.Join(want, "\n"); got != w {
		t.Errorf("updates\n%s\nwant\n%s", got, w)
	}
}

// A command Codex can't read as one action is a terminal: its output
// streams as terminal_output_delta pieces and it ends with terminal_exit,
// as codex-acp sends them; what it typed in is a piece too.
func TestTerminalCommand(t *testing.T) {
	unknown := []map[string]any{{"type": "unknown", "command": "echo hi"}}
	got := runTurn(t,
		itemNote("item/started", command("inProgress", unknown, nil, nil)),
		delta("item/commandExecution/outputDelta", "call_1", "hi\n"),
		note{Method: "item/commandExecution/terminalInteraction", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_1", "processId": "p", "stdin": "y"}},
		itemNote("item/completed", command("completed", unknown, "hi\n", 0)))
	check(t, got,
		`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"echo hi","name":"exec_command","kind":"execute","status":"in_progress",`+
			`"content":[{"type":"terminal","terminalId":"call_1"}],"rawInput":{"command":"echo hi","cwd":"/w/proj"},"_meta":{"terminal_info":{"cwd":"/w/proj","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","_meta":{"terminal_output_delta":{"data":"hi\n","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","_meta":{"terminal_output_delta":{"data":"\ny\n","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"completed","name":"exec_command","_meta":{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"call_1"}}}`)
}

// A failed command's output that never streamed comes once at its end,
// with its exit code; a command of several actions is a terminal too.
func TestCommandOutputAtTheEnd(t *testing.T) {
	two := []map[string]any{{"type": "read", "command": "cat a", "name": "a", "path": "/w/proj/a"}, {"type": "unknown", "command": "wc"}}
	got := runTurn(t, itemNote("item/completed", command("failed", two, "boom\n", 2)))
	check(t, got,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"failed","name":"exec_command","_meta":{"terminal_exit":{"exit_code":2,"signal":null,"terminal_id":"call_1"},"terminal_output_delta":{"data":"boom\n","terminal_id":"call_1"}}}`)
}

// A read, a search and a listing are calls of their kind with no
// terminal; their exit code is rawOutput.
func TestReadSearchList(t *testing.T) {
	read := []map[string]any{{"type": "read", "command": "cat a.go", "name": "a.go", "path": "/w/proj/a.go"}}
	search := []map[string]any{{"type": "search", "command": "rg x", "query": "x", "path": "src"}}
	list := []map[string]any{{"type": "listFiles", "command": "ls", "path": nil}}
	got := runTurn(t,
		itemNote("item/started", command("inProgress", read, nil, nil)),
		itemNote("item/completed", command("completed", read, "package a\n", 0)),
		itemNote("item/started", command("inProgress", search, nil, nil)),
		itemNote("item/started", command("inProgress", list, nil, nil)))
	check(t, got,
		`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"Read file '/w/proj/a.go'","name":"exec_command","kind":"read","status":"in_progress","locations":[{"path":"/w/proj/a.go"}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"completed","name":"exec_command","rawOutput":{"exit_code":0},"_meta":{"terminal_output_delta":{"data":"package a\n","terminal_id":"call_1"}}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"Search for 'x' in src","name":"exec_command","kind":"search","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"List files","name":"exec_command","kind":"read","status":"in_progress"}`)
}

// A file change shows each file's diff: a new file's text, and an
// update's hunks under the path it moves to.
func TestFileChange(t *testing.T) {
	change := func(status string) map[string]any {
		return map[string]any{"type": "fileChange", "id": "call_2", "status": status, "changes": []any{
			map[string]any{"path": "/w/proj/new.txt", "kind": map[string]any{"type": "add"}, "diff": "hello\n"},
			map[string]any{"path": "/w/proj/a.txt", "kind": map[string]any{"type": "update", "move_path": "/w/proj/b.txt"},
				"diff": "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n@@ -9 +9 @@\n-x\n\\ No newline at end of file\n+y\n\\ No newline at end of file\n\n\nMoved to: /w/proj/b.txt"},
		}}
	}
	got := runTurn(t, itemNote("item/started", change("inProgress")), itemNote("item/completed", change("completed")))
	check(t, got,
		`{"sessionUpdate":"tool_call","toolCallId":"call_2","title":"Editing files","kind":"edit","status":"in_progress","content":[`+
			`{"type":"diff","path":"/w/proj/new.txt","newText":"hello\n","_meta":{"kind":"add"}},`+
			`{"type":"diff","path":"/w/proj/b.txt","oldText":"one\ntwo\n","newText":"one\nTWO\n","_meta":{"kind":"update"}},`+
			`{"type":"diff","path":"/w/proj/b.txt","oldText":"x","newText":"y","_meta":{"kind":"update"}}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_2","status":"completed"}`)
}

// MCP calls, web searches, image views and compactions are calls as
// codex-acp makes them.
func TestOtherTools(t *testing.T) {
	mcp := func(status string, result any) map[string]any {
		return map[string]any{"type": "mcpToolCall", "id": "call_3", "server": "notes", "tool": "show", "status": status, "arguments": map[string]any{"path": "a.md"},
			"result": result, "error": nil, "durationMs": nil, "appContext": nil, "mcpAppUi": nil, "pluginId": nil, "readOnlyHint": nil}
	}
	search := map[string]any{"type": "webSearch", "id": "ws_1", "query": "go", "action": map[string]any{"type": "openPage", "url": "https://go.dev"}}
	got := runTurn(t,
		itemNote("item/started", mcp("inProgress", nil)),
		note{Method: "item/mcpToolCall/progress", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_3", "message": " half \n"}},
		itemNote("item/completed", mcp("completed", map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "structuredContent": nil, "_meta": nil})),
		itemNote("item/started", search),
		itemNote("item/started", map[string]any{"type": "imageView", "id": "iv_1", "path": "/w/proj/a.png"}),
		itemNote("item/completed", map[string]any{"type": "imageView", "id": "iv_1", "path": "/w/proj/a.png"}),
		itemNote("item/started", map[string]any{"type": "contextCompaction", "id": "cc_1"}),
		itemNote("item/completed", map[string]any{"type": "contextCompaction", "id": "cc_1"}))
	check(t, got,
		`{"sessionUpdate":"tool_call","toolCallId":"call_3","title":"mcp.notes.show","kind":"execute","status":"in_progress","rawInput":{"arguments":{"path":"a.md"},"server":"notes","tool":"show"},"_meta":{"is_mcp_tool_call":true}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_3","_meta":{"mcp_output_delta":{"data":"half"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_3","status":"completed","rawInput":{"arguments":{"path":"a.md"},"server":"notes","tool":"show"},`+
			`"rawOutput":{"error":null,"result":{"_meta":null,"content":[{"text":"ok","type":"text"}],"structuredContent":null}},"_meta":{"is_mcp_tool_call":true}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"ws_1","title":"Open page: https://go.dev","kind":"search","status":"in_progress","rawInput":{"action":{"type":"openPage","url":"https://go.dev"},"id":"ws_1","query":"go","type":"webSearch"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"iv_1","title":"View Image /w/proj/a.png","name":"view_image","kind":"read","status":"completed","content":[{"type":"content","content":{"type":"resource_link","name":"/w/proj/a.png","uri":"/w/proj/a.png"}}],"locations":[{"path":"/w/proj/a.png"}],"rawInput":{"path":"/w/proj/a.png"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"cc_1","title":"Compact conversation","kind":"think","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"cc_1","status":"completed","title":"Compact conversation"}`)
}
