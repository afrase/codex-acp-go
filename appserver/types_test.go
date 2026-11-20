package appserver

import (
	"encoding/json"
	"testing"
)

// Items decode by their kind's fields, with the item kept as sent.
func TestItemsDecode(t *testing.T) {
	var items []Item
	src := `[
	{"type":"userMessage","id":"u1","clientId":null,"content":[{"type":"text","text":"hi","text_elements":[]},{"type":"image","url":"data:image/png;base64,AA=="}]},
	{"type":"reasoning","id":"rs_1","summary":["Thinking"],"content":["raw"]},
	{"type":"commandExecution","id":"call_1","command":"/bin/zsh -lc 'ls'","cwd":"/w/proj","processId":null,"source":"agent","status":"completed",
	 "commandActions":[{"type":"listFiles","command":"ls","path":null}],"aggregatedOutput":"a\n","exitCode":0,"durationMs":3},
	{"type":"fileChange","id":"call_2","status":"completed","changes":[{"path":"/w/proj/a.txt","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-a\n+b\n"}]}
	]`
	if err := json.Unmarshal([]byte(src), &items); err != nil {
		t.Fatal(err)
	}
	in := items[0].Inputs()
	if len(in) != 2 || in[0].Text != "hi" || in[1].URL != "data:image/png;base64,AA==" {
		t.Errorf("user inputs %+v", in)
	}
	if r := items[1]; r.Summary[0] != "Thinking" || r.ReasoningText()[0] != "raw" {
		t.Errorf("reasoning %+v", r)
	}
	c := items[2]
	if c.Command != "/bin/zsh -lc 'ls'" || *c.ExitCode != 0 || *c.AggregatedOutput != "a\n" || c.CommandActions[0].Type != "listFiles" || c.CommandActions[0].Path != nil {
		t.Errorf("command %+v", c)
	}
	if f := items[3]; f.Changes[0].Kind.Type != "update" || f.Changes[0].Kind.MovePath != nil {
		t.Errorf("file change %+v", f)
	}
	var raw map[string]any
	if err := json.Unmarshal(items[2].Raw, &raw); err != nil || raw["durationMs"] != 3.0 {
		t.Errorf("raw %s", items[2].Raw)
	}
}

// Text input carries text_elements, even empty, as Codex requires; an
// image doesn't.
func TestUserInputJSON(t *testing.T) {
	b, err := json.Marshal([]UserInput{{Type: "text", Text: "hi"}, {Type: "image", URL: "https://x/y.png"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `[{"type":"text","text":"hi","text_elements":[]},{"type":"image","url":"https://x/y.png"}]` {
		t.Errorf("got %s", got)
	}
}

// Codex has a login when it has an account or its provider needs none.
func TestSignedIn(t *testing.T) {
	for src, want := range map[string]bool{
		`{"account":null,"requiresOpenaiAuth":true}`:              false,
		`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`: true,
		`{"account":null,"requiresOpenaiAuth":false}`:             true,
	} {
		var r AccountReadResponse
		if err := json.Unmarshal([]byte(src), &r); err != nil {
			t.Fatal(err)
		}
		if r.SignedIn() != want {
			t.Errorf("%s: signed in %v", src, !want)
		}
	}
}

// A turn's error info is read when it's a plain string.
func TestErrorInfo(t *testing.T) {
	var n TurnNotification
	src := `{"threadId":"thr_1","turn":{"id":"t1","items":[],"status":"failed","error":{"message":"no","codexErrorInfo":"unauthorized","additionalDetails":null}}}`
	if err := json.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	if n.Turn.Error.ErrorInfo() != "unauthorized" {
		t.Errorf("info %q", n.Turn.Error.ErrorInfo())
	}
	var other TurnError
	_ = json.Unmarshal([]byte(`{"message":"x","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":502}}}`), &other)
	if other.ErrorInfo() != "" {
		t.Errorf("object info read as %q", other.ErrorInfo())
	}
}
