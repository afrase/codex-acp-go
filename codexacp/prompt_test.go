package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

type note = appservertest.Note

func delta(method, item, text string) note {
	return note{Method: method, Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": item, "delta": text}}
}

func usageNote(total, last map[string]int, window any) note {
	count := func(m map[string]int) map[string]any {
		return map[string]any{"totalTokens": m["total"], "inputTokens": m["input"], "cachedInputTokens": m["cached"],
			"cacheWriteInputTokens": 0, "outputTokens": m["output"], "reasoningOutputTokens": m["reasoning"]}
	}
	return note{Method: "thread/tokenUsage/updated", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1",
		"tokenUsage": map[string]any{"total": count(total), "last": count(last), "modelContextWindow": window}}}
}

func itemNote(method string, item map[string]any) note {
	return note{Method: method, Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "item": item, "startedAtMs": 1, "completedAtMs": 2}}
}

// A prompt is a turn in the session's mode, model and effort; its text
// and thoughts stream as chunks whose message id is Codex's item, its
// title and context use come as Codex says them, and it ends at
// turn/completed with what it used.
func TestPrompt(t *testing.T) {
	f := codex()
	f.Turn("turn_1", "completed",
		itemNote("item/started", map[string]any{"type": "reasoning", "id": "rs_1", "summary": []string{}, "content": []string{}}),
		delta("item/reasoning/summaryTextDelta", "rs_1", "Looking"),
		note{Method: "item/reasoning/summaryPartAdded", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "rs_1", "summaryIndex": 1}},
		itemNote("item/completed", map[string]any{"type": "reasoning", "id": "rs_1", "summary": []string{"Looking"}, "content": []string{}}),
		delta("item/agentMessage/delta", "msg_1", "Hello"),
		delta("item/agentMessage/delta", "msg_1", " there"),
		usageNote(map[string]int{"total": 150, "input": 120, "cached": 20, "output": 30, "reasoning": 5},
			map[string]int{"total": 150, "input": 120, "cached": 20, "output": 30, "reasoning": 5}, 1000),
		note{Method: "thread/name/updated", Params: map[string]any{"threadId": "thr_1", "threadName": "Say\n hello"}},
		note{Method: "turn/plan/updated", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "explanation": nil,
			"plan": []any{map[string]any{"step": "Look", "status": "completed"}, map[string]any{"step": "Say", "status": "inProgress"}}}},
	)
	h := initialized(t, f)
	sid := h.session()
	set := func(id, v string) {
		_, err := h.conn.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: acp.SessionID(sid), ConfigID: acp.SessionConfigID(id),
			ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: acp.SessionConfigValueID(v)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	set("mode", "agent-full-access")
	set("reasoning_effort", "low")
	resp, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"threadId":"thr_1","input":[{"type":"text","text":"hi","text_elements":[]}],"approvalPolicy":"never","approvalsReviewer":"user",` +
		`"sandboxPolicy":{"type":"dangerFullAccess"},"model":"fake-model-1","effort":"low","summary":"auto"}`
	if got := string(f.Calls("turn/start")[0]); got != want {
		t.Errorf("turn/start\n%s\nwant\n%s", got, want)
	}
	if resp.StopReason != acp.StopReasonEndTurn || jsonOf(t, resp.Usage) != `{"totalTokens":150,"inputTokens":100,"outputTokens":30,"thoughtTokens":5,"cachedReadTokens":20,"cachedWriteTokens":0}` {
		t.Errorf("prompt answered %s", jsonOf(t, resp))
	}
	wantUpdates := strings.Join([]string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Looking"},"messageId":"rs_1"}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"\n\n"},"messageId":"rs_1"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello"},"messageId":"msg_1"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":" there"},"messageId":"msg_1"}`,
		`{"sessionUpdate":"usage_update","used":150,"size":1000}`,
		`{"sessionUpdate":"session_info_update","title":"Say hello"}`,
		`{"sessionUpdate":"plan","entries":[{"content":"Look","priority":"medium","status":"completed"},{"content":"Say","priority":"medium","status":"in_progress"}]}`,
	}, "\n")
	if got := h.waitUpdates(7); got != wantUpdates {
		t.Errorf("updates\n%s\nwant\n%s", got, wantUpdates)
	}
}

// A second turn's usage is the thread's total less the first's; a
// reasoning that came whole is sent whole.
func TestSecondTurnUsage(t *testing.T) {
	f := codex()
	h := initialized(t, f)
	sid := h.session()
	prompt := func() *acp.PromptResponse {
		resp, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	f.Turn("turn_1", "completed", usageNote(map[string]int{"total": 100, "input": 80, "output": 20}, map[string]int{"total": 100, "input": 80, "output": 20}, nil))
	prompt()
	f.Turn("turn_1", "completed",
		itemNote("item/completed", map[string]any{"type": "reasoning", "id": "rs_2", "summary": []string{"One", "", "Two"}, "content": []string{}}),
		usageNote(map[string]int{"total": 130, "input": 100, "output": 30}, map[string]int{"total": 30, "input": 20, "output": 10}, nil))
	if u := prompt().Usage; u.TotalTokens != 30 || u.InputTokens != 20 || u.OutputTokens != 10 {
		t.Errorf("second turn's usage %s", jsonOf(t, u))
	}
	if got := h.updates(); got != `{"sessionUpdate":"session_info_update","title":"go"}`+"\n"+`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"One\n\nTwo"},"messageId":"rs_2"}` {
		t.Errorf("updates %s", got)
	}
}

// A prompt's pieces become Codex input as codex-acp makes them.
func TestPromptInput(t *testing.T) {
	blocks := []acp.ContentBlock{
		textBlock("look"),
		{Type: "image", Image: &acp.ImageContent{Data: "AAA=", MimeType: "image/png"}},
		{Type: "image", Image: &acp.ImageContent{URI: "https://x/y.png", MimeType: "image/png", Data: "AAA="}},
		{Type: "resource_link", ResourceLink: &acp.ResourceLink{Name: "a.go", URI: "file:///w/proj/a.go"}},
		{Type: "resource_link", ResourceLink: &acp.ResourceLink{URI: "file:///w/proj/b.go"}},
		{Type: "resource", Resource: &acp.EmbeddedResource{Resource: acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{URI: "file:///w/c.txt", Text: "C"}}}},
		{Type: "resource", Resource: &acp.EmbeddedResource{Resource: acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{URI: "file:///w/d.png", MimeType: "image/png", Blob: "BB=="}}}},
		{Type: "audio", Audio: &acp.AudioContent{Data: "x", MimeType: "audio/wav"}},
	}
	want := `[{"type":"text","text":"look","text_elements":[]},{"type":"image","url":"data:image/png;base64,AAA="},{"type":"image","url":"https://x/y.png"},` +
		`{"type":"text","text":"[@a.go](file:///w/proj/a.go)","text_elements":[]},{"type":"text","text":"[@b.go](file:///w/proj/b.go)","text_elements":[]},` +
		`{"type":"text","text":"[@c.txt](file:///w/c.txt)\n\u003ccontext ref=\"file:///w/c.txt\"\u003e\nC\n\u003c/context\u003e","text_elements":[]},` +
		`{"type":"image","url":"data:image/png;base64,BB=="}]`
	if got := jsonOf(t, promptInput(blocks)); got != want {
		t.Errorf("input\n%s\nwant\n%s", got, want)
	}
}

// An image goes only to a model that takes images.
func TestImageNeedsAModelThatTakesIt(t *testing.T) {
	f := codex()
	h := initialized(t, f)
	sid := h.session()
	img := []acp.ContentBlock{{Type: "image", Image: &acp.ImageContent{Data: "AAA=", MimeType: "image/png"}}}
	_, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: img})
	var e *acp.Error
	if !errors.As(err, &e) || e.Code != acp.ErrorCodeInvalidRequest {
		t.Errorf("image to a text model: %v", err)
	}
	if _, err := h.conn.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: acp.SessionID(sid), ConfigID: "model",
		ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: appservertest.Model2}}); err != nil {
		t.Fatal(err)
	}
	f.Turn("turn_1", "completed")
	if _, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: img}); err != nil {
		t.Errorf("image to an image model: %v", err)
	}
}

// A turn Codex ended before turn/start's answer still ends the prompt; a
// failed turn ends it too, its error shown as text, and a missing login
// fails it as Authentication required.
func TestTurnEnds(t *testing.T) {
	f := codex()
	h := initialized(t, f)
	sid := h.session()
	prompt := func() (*acp.PromptResponse, error) {
		return h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
	}
	f.On("turn/start", func(json.RawMessage) (any, error) {
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_9", "completed")})
		return map[string]any{"turn": appservertest.TurnOf("turn_9", "inProgress")}, nil
	})
	if resp, err := prompt(); err != nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("ended before its answer: %v, %v", resp, err)
	}
	failed := appservertest.TurnOf("turn_1", "failed")
	failed["error"] = map[string]any{"message": "boom", "codexErrorInfo": "other"}
	f.Turn("turn_1", "", note{Method: "error", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "willRetry": true, "error": map[string]any{"message": "retrying"}}},
		note{Method: "error", Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "willRetry": false,
			"error": map[string]any{"message": `{"type":"error","status":503,"error":{"type":"server_error","message":"Overloaded"}}`}}},
		note{Method: "turn/completed", Params: map[string]any{"threadId": "thr_1", "turn": failed}})
	if resp, err := prompt(); err != nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("failed turn: %v, %v", resp, err)
	}
	if got := h.updates(); got != `{"sessionUpdate":"session_info_update","title":"go"}`+"\n"+`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Overloaded\n\n"}}` {
		t.Errorf("updates %s", got)
	}
	unauthorized := appservertest.TurnOf("turn_1", "failed")
	unauthorized["error"] = map[string]any{"message": "401", "codexErrorInfo": "unauthorized"}
	f.Turn("turn_1", "", note{Method: "turn/completed", Params: map[string]any{"threadId": "thr_1", "turn": unauthorized}})
	var e *acp.Error
	if _, err := prompt(); !errors.As(err, &e) || e.Code != acp.ErrorCodeAuthenticationRequired {
		t.Errorf("unauthorized turn: %v", err)
	}
}

// A warning is agent text without a message id.
func TestWarnings(t *testing.T) {
	f := codex()
	f.Turn("turn_1", "completed",
		note{Method: "warning", Params: map[string]any{"threadId": "thr_1", "message": "Careful"}},
		note{Method: "configWarning", Params: map[string]any{"summary": "Bad key", "details": "line 3"}})
	h := initialized(t, f)
	sid := h.session()
	if _, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}}); err != nil {
		t.Fatal(err)
	}
	want := `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Warning: Careful\n\n"}}` + "\n" +
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Config warning: Bad key\n\nline 3\n\n"}}` + "\n" +
		`{"sessionUpdate":"session_info_update","title":"go"}`
	if got := h.waitUpdates(3); got != want {
		t.Errorf("updates\n%s\nwant\n%s", got, want)
	}
}

// A live item that doesn't decode is logged and skipped, one of a kind
// the agent doesn't know shows nothing, and the turn goes on to its end,
// even when turn/completed's own items don't decode.
func TestPromptSkipsBadItems(t *testing.T) {
	f := codex()
	completed := appservertest.TurnOf("turn_1", "completed")
	completed["items"] = []any{map[string]any{"type": "agentMessage", "id": "msg_1", "text": 7}}
	f.Turn("turn_1", "",
		itemNote("item/started", map[string]any{"type": "commandExecution", "id": "call_bad", "command": []string{"secret-word"}, "status": "inProgress"}),
		itemNote("item/started", map[string]any{"type": "somethingNew", "id": "new_1"}),
		delta("item/agentMessage/delta", "msg_1", "Done"),
		note{Method: "turn/completed", Params: map[string]any{"threadId": "thr_1", "turn": completed}})
	h := initialized(t, f)
	sid := h.session()
	resp, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
	if err != nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("prompt: %v, %v", resp, err)
	}
	want := `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Done"},"messageId":"msg_1"}` + "\n" +
		`{"sessionUpdate":"session_info_update","title":"go"}`
	if got := h.waitUpdates(2); got != want {
		t.Errorf("updates\n%s\nwant\n%s", got, want)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "method=item/started") || !strings.Contains(logs, "commandExecution") || strings.Contains(logs, "secret-word") {
		t.Errorf("log\n%s", logs)
	}
}
