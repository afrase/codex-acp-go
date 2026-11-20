package codexacp

import (
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// A new session's commands are its folder's skills as `$<name>`, sent once
// its answer is out; a turn that leaves them as they were sends none.
func TestCommands(t *testing.T) {
	f := codex()
	f.On("skills/list", appservertest.Reply(map[string]any{"data": []any{map[string]any{"cwd": "/w/proj", "errors": []any{}, "skills": []any{
		map[string]any{"name": "lint", "description": "Lints the code", "shortDescription": "Lint", "path": "/w/proj/.agents/skills/lint/SKILL.md", "scope": "repo", "enabled": true, "pluginId": nil},
		map[string]any{"name": "docs", "description": "Writes docs", "path": "/h/.agents/skills/docs/SKILL.md", "scope": "user", "enabled": true, "pluginId": nil},
	}}}}))
	f.Turn("turn_1", "completed")
	h := initialized(t, f)
	sid := h.session()
	want := `{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"$lint","description":"Lint"},{"name":"$docs","description":"Writes docs"}]}`
	if got := h.waitUpdates(1); got != want {
		t.Errorf("updates %s", got)
	}
	if got := string(f.Calls("skills/list")[0]); got != `{"cwds":["/w/proj"]}` {
		t.Errorf("skills/list %s", got)
	}
	if _, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}}); err != nil {
		t.Fatal(err)
	}
	if got := h.updates(); got != want+"\n"+`{"sessionUpdate":"session_info_update","title":"go"}` {
		t.Errorf("after the turn: %s", got)
	}
}
