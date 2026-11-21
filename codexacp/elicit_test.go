package codexacp

import (
	"testing"

	acp "github.com/afrase/acp-go/unstable"
)

func accept(content map[string]any) func(*acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error) {
	return func(*acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error) {
		values := map[string]acp.ElicitationContentValue{}
		for k, v := range content {
			s := v.(string)
			values[k] = acp.ElicitationContentValue{String: &s}
		}
		return &acp.CreateElicitationResponse{Action: "accept", Accept: &acp.ElicitationAcceptAction{Content: values}}, nil
	}
}

// Plan mode's questions are one form tied to their item: each a required
// string, choices with "None of the above" and an optional `<id>_note`
// when it takes another answer; Codex gets the choice, then the note.
func TestQuestions(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	askTurn(f, "item/tool/requestUserInput", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_q", "isBlocking": true, "autoResolutionMs": nil,
		"questions": []any{
			map[string]any{"id": "color", "header": "Color", "question": "Which color?", "isOther": true, "isSecret": false,
				"options": []any{map[string]any{"label": "Red", "description": "Warm"}, map[string]any{"label": "Blue", "description": ""}}},
			map[string]any{"id": "name", "header": "", "question": "Its name?", "isOther": false, "isSecret": false, "options": nil},
		}}, answers)
	h := initialized(t, f)
	h.c.elicit = accept(map[string]any{"color": "None of the above", "color_note": " teal ", "name": "Bob"})
	sid := h.session()
	h.prompt(sid, "go")
	if got := <-answers; got != `{"answers":{"color":{"answers":["None of the above","user_note: teal"]},"name":{"answers":["Bob"]}}}` {
		t.Errorf("Codex was answered %s", got)
	}
	want := `{"message":"Codex needs your input to continue.","_meta":{"codex":{"autoResolutionMs":null}},"mode":"form","requestedSchema":{"type":"object","properties":{` +
		`"color":{"type":"string","title":"Which color?","description":"Color","oneOf":[{"const":"Red","title":"Red","description":"Warm"},{"const":"Blue","title":"Blue"},` +
		`{"const":"None of the above","title":"None of the above","description":"Provide a different answer in the note field."}],"_meta":{"codex":{"isOther":true,"isSecret":false}}},` +
		`"color_note":{"type":"string","title":"Additional answer or note","_meta":{"codex":{"isSecret":false,"questionId":"color","role":"user_note"}}},` +
		`"name":{"type":"string","title":"Its name?","_meta":{"codex":{"isOther":false,"isSecret":false}}}},"required":["color","name"]},"sessionId":"thr_1","toolCallId":"call_q"}`
	if got := h.c.forms[0]; got != want {
		t.Errorf("form\n%s\nwant\n%s", got, want)
	}
}

// An MCP tool's approval is a permission request about the tool's
// running call, offering what the server lets it keep; the option picked
// is Codex's answer, and an allowed call runs on.
func TestMCPToolApproval(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	call := map[string]any{"type": "mcpToolCall", "id": "call_3", "server": "notes", "tool": "show", "status": "inProgress", "arguments": map[string]any{},
		"result": nil, "error": nil}
	askTurn(f, "mcpServer/elicitation/request", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "serverName": "notes", "mode": "form",
		"message": "Allow show?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"_meta": map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": []any{"session", "always"}}}, answers,
		itemNote("item/started", call))
	h := initialized(t, f)
	h.c.permit = pick("allow_session")
	sid := h.session()
	h.prompt(sid, "go")
	if got := <-answers; got != `{"_meta":{"persist":"session"},"action":"accept","content":null}` {
		t.Errorf("Codex was answered %s", got)
	}
	want := `{"sessionId":"thr_1","toolCall":{"toolCallId":"call_3","kind":"execute","status":"pending"},"options":[` +
		`{"optionId":"allow_once","name":"Allow","kind":"allow_once"},{"optionId":"allow_session","name":"Allow for this session","kind":"allow_always"},` +
		`{"optionId":"allow_always","name":"Always allow","kind":"allow_always"},{"optionId":"cancel","name":"Cancel","kind":"reject_once"}],"_meta":{"is_mcp_tool_approval":true}}`
	if got := jsonOf(t, h.waitAsked(1)); got != want {
		t.Errorf("asked\n%s\nwant\n%s", got, want)
	}
	if got := h.waitUpdates(3); got != `{"sessionUpdate":"tool_call","toolCallId":"call_3","title":"mcp.notes.show","kind":"execute","status":"in_progress","rawInput":{"arguments":{},"server":"notes","tool":"show"},"_meta":{"is_mcp_tool_call":true}}`+"\n"+
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_3","status":"in_progress"}`+"\n"+`{"sessionUpdate":"session_info_update","title":"go"}` {
		t.Errorf("updates %s", got)
	}
}

// An MCP server's form with fields is a form, its enum with names as
// choices; what the user filled in goes to Codex.
func TestMCPForm(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	askTurn(f, "mcpServer/elicitation/request", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "serverName": "db", "mode": "form", "_meta": nil,
		"message": "Pick an env", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{
			"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}, "enumNames": []any{"Dev", "Prod"}}}}}, answers)
	h := initialized(t, f)
	h.c.elicit = accept(map[string]any{"env": "dev"})
	sid := h.session()
	h.prompt(sid, "go")
	if got := <-answers; got != `{"_meta":null,"action":"accept","content":{"env":"dev"}}` {
		t.Errorf("Codex was answered %s", got)
	}
	if got := h.c.forms[0]; got != `{"message":"Pick an env","mode":"form","requestedSchema":{"type":"object","properties":{"env":{"type":"string","oneOf":[{"const":"dev","title":"Dev"},{"const":"prod","title":"Prod"}]}}},"sessionId":"thr_1"}` {
		t.Errorf("form %s", got)
	}
}

// A client without forms gets no questions: Codex hears no answers.
func TestQuestionsWithoutForms(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	askTurn(f, "item/tool/requestUserInput", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_q", "isBlocking": true, "autoResolutionMs": nil,
		"questions": []any{map[string]any{"id": "x", "header": "", "question": "?", "isOther": false, "isSecret": false, "options": nil}}}, answers)
	h := start(t, f)
	if _, err := h.conn.Initialize(t.Context(), &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	sid := h.session()
	h.prompt(sid, "go")
	if got := <-answers; got != `{"answers":{}}` {
		t.Errorf("Codex was answered %s", got)
	}
}
