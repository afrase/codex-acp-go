package codexacp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// approvalTurn scripts a turn that asks method with params, then keeps
// Codex's answer in answers and completes.
func approvalTurn(t *testing.T, f *appservertest.Fake, method string, params map[string]any, answers chan<- string) {
	t.Helper()
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.Then("turn/start", func(json.RawMessage) {
		f.Notify("turn/started", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "inProgress")})
		answer, err := f.Ask(context.Background(), method, params)
		if err != nil {
			answers <- err.Error()
		} else {
			answers <- string(answer)
		}
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "completed")})
	})
}

func pick(id string) func(*acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
	return func(*acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
		return &acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Outcome: "selected",
			Selected: &acp.SelectedPermissionOutcome{OptionID: acp.PermissionOptionID(id)}}}, nil
	}
}

// A command's approval is a permission request before its tool call,
// with Codex's decisions as options, allowing first; the option picked
// is Codex's decision.
func TestCommandApproval(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	approvalTurn(t, f, "item/commandExecution/requestApproval", map[string]any{
		"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_1", "startedAtMs": 1, "environmentId": nil, "kind": "command",
		"command": "/bin/zsh -lc 'npm test'", "cwd": "/w/proj", "reason": "needs the network",
		"commandActions":              []any{map[string]any{"type": "unknown", "command": "npm test"}},
		"proposedExecpolicyAmendment": []string{"npm", "test"},
	}, answers)
	h := initialized(t, f)
	h.c.permit = pick("accept_execpolicy_amendment")
	sid := h.session()
	if _, err := h.conn.Prompt(t.Context(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("test")}}); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != `{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["npm","test"]}}}` {
		t.Errorf("Codex was answered %s", got)
	}
	req := h.waitAsked(1)
	want := `{"sessionId":"thr_1","toolCall":{"toolCallId":"call_1","kind":"execute","status":"pending","title":"Run command","rawInput":{"command":"npm test","cwd":"/w/proj"}},"options":[` +
		`{"optionId":"allow_once","name":"Yes, proceed","kind":"allow_once"},` +
		`{"optionId":"allow_for_session","name":"Yes, and don't ask again for this command in this session","kind":"allow_always"},` +
		"{\"optionId\":\"accept_execpolicy_amendment\",\"name\":\"Yes, and don't ask again for commands that start with `npm test`\",\"kind\":\"allow_always\"}," +
		`{"optionId":"decline","name":"No, continue without running it","kind":"reject_once"},` +
		`{"optionId":"cancel","name":"No, and tell Codex what to do differently","kind":"reject_once"}]}`
	if got := jsonOf(t, req); got != want {
		t.Errorf("asked\n%s\nwant\n%s", got, want)
	}
}

// A file change's approval names its files, from the change's start;
// Codex's own list of decisions, when it sends one, is the options.
func TestFileChangeApproval(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.Then("turn/start", func(json.RawMessage) {
		f.Notify("item/started", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "item": map[string]any{"type": "fileChange", "id": "call_2",
			"status": "inProgress", "changes": []any{map[string]any{"path": "/w/proj/a.txt", "kind": map[string]any{"type": "add"}, "diff": "a\n"}}}})
		answer, _ := f.Ask(context.Background(), "item/fileChange/requestApproval", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_2", "startedAtMs": 1})
		answers <- string(answer)
		answer, _ = f.Ask(context.Background(), "item/commandExecution/requestApproval", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_3",
			"startedAtMs": 1, "environmentId": nil, "kind": "command", "command": "cat a", "commandActions": []any{map[string]any{"type": "read", "command": "cat a", "name": "a", "path": "/w/proj/a"}},
			"availableDecisions": []any{"accept", "decline"}})
		answers <- string(answer)
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "completed")})
	})
	h := initialized(t, f)
	h.c.permit = pick("allow_for_session")
	sid := h.session()
	go func() {
		_, _ = h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("edit")}})
	}()
	if got := <-answers; got != `{"decision":"acceptForSession"}` {
		t.Errorf("file change answered %s", got)
	}
	want := `{"sessionId":"thr_1","toolCall":{"toolCallId":"call_2","kind":"edit","status":"pending","title":"Edit files","locations":[{"path":"/w/proj/a.txt"}]},"options":[` +
		`{"optionId":"allow_once","name":"Yes, proceed","kind":"allow_once"},{"optionId":"allow_for_session","name":"Yes, and don't ask again for these files","kind":"allow_always"},` +
		`{"optionId":"cancel","name":"No, and tell Codex what to do differently","kind":"reject_once"}]}`
	if got := jsonOf(t, h.waitAsked(1)); got != want {
		t.Errorf("asked\n%s\nwant\n%s", got, want)
	}
	h.c.mu.Lock()
	h.c.permit = pick("decline")
	h.c.mu.Unlock()
	if got := <-answers; got != `{"decision":"decline"}` {
		t.Errorf("read answered %s", got)
	}
	if got := jsonOf(t, h.waitAsked(2).ToolCall); got != `{"toolCallId":"call_3","kind":"execute","status":"pending","title":"Read file","locations":[{"path":"/w/proj/a"}],"rawInput":{"command":"cat a"}}` {
		t.Errorf("read's call %s", got)
	}
}

// Cancel withdraws the question the user hasn't answered (Codex hears
// cancel), interrupts the turn, and the prompt ends cancelled.
func TestCancel(t *testing.T) {
	f := codex()
	answers := make(chan string, 1)
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.On("turn/interrupt", appservertest.Reply(map[string]any{}))
	h := initialized(t, f)
	sid := h.session()
	f.Then("turn/start", func(json.RawMessage) {
		f.Notify("turn/started", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "inProgress")})
		answer, _ := f.Ask(context.Background(), "item/fileChange/requestApproval", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "call_2", "startedAtMs": 1})
		answers <- string(answer)
	})
	f.Then("turn/interrupt", func(json.RawMessage) {
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "interrupted")})
	})
	type result struct {
		resp *acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("edit")}})
		done <- result{resp, err}
	}()
	h.waitAsked(1)
	if err := h.conn.Cancel(t.Context(), &acp.CancelNotification{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != `{"decision":"cancel"}` {
		t.Errorf("Codex was answered %s", got)
	}
	r := <-done
	if r.err != nil || r.resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("prompt: %v, %v", r.resp, r.err)
	}
	if got := f.Calls("turn/interrupt"); len(got) != 1 || string(got[0]) != `{"threadId":"thr_1","turnId":"turn_1"}` {
		t.Errorf("interrupt %s", got)
	}
}

// A second prompt waits for the turn running to end, as Codex runs one
// turn at a time.
func TestPromptsOneAtATime(t *testing.T) {
	f := codex()
	release := make(chan struct{})
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.Then("turn/start", func(json.RawMessage) {
		<-release
		f.Notify("turn/completed", map[string]any{"threadId": "thr_1", "turn": appservertest.TurnOf("turn_1", "completed")})
	})
	h := initialized(t, f)
	sid := h.session()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
			done <- err
		}()
	}
	if _, err := f.Await(t.Context(), "turn/start", 0); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Calls("turn/start")); n != 1 {
		t.Errorf("%d turns started at once", n)
	}
	release <- struct{}{}
	if _, err := f.Await(t.Context(), "turn/start", 1); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// An "always" label names exactly what the amendment allows: a
// PowerShell script alone only when it is the last word, and a backtick
// in it can't end the label's code span.
func TestExecPolicyLabel(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"/bin/zsh", "-lc", "npm test"}, "`npm test`"},
		{[]string{"pwsh", "-NoProfile", "-Command", "Get-Item ."}, "`Get-Item .`"},
		{[]string{"pwsh", "-Command", "Get-Item .", "-NoExit"}, "`pwsh -Command 'Get-Item .' -NoExit`"},
		{[]string{"powershell.exe", "-c", "Write-Host `a"}, "``Write-Host `a``"},
		{[]string{"echo", "`x`"}, "``echo '`x`'``"},
		{[]string{"bash", "-c", "`pwd`"}, "`` `pwd` ``"},
	} {
		offers, ok := commandOffers(appserver.CommandApproval{ProposedExecpolicyAmendment: c.argv})
		if !ok {
			t.Fatalf("%q: no choice", c.argv)
		}
		want := "Yes, and don't ask again for commands that start with " + c.want
		if got := offers[2].option.Name; got != want {
			t.Errorf("%q:\n%s\nwant\n%s", c.argv, got, want)
		}
	}
}

// A network rule is offered only for the host the request shows and
// with an action Codex's schema has; the one-time options stay.
func TestNetworkAmendmentOffered(t *testing.T) {
	rule := func(host, action string) json.RawMessage {
		return json.RawMessage(`{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"` + host + `","action":"` + action + `"}}}`)
	}
	for _, c := range []struct {
		rule json.RawMessage
		want string
	}{
		{rule("example.com", "allow"), "allow_once,allow_for_session,apply_network_policy_amendment:0,decline"},
		{rule("example.com", "deny"), "allow_once,allow_for_session,apply_network_policy_amendment:0,decline"},
		{rule("evil.test", "allow"), "allow_once,allow_for_session,decline"},
		{rule("example.com", "allowAll"), "allow_once,allow_for_session,decline"},
	} {
		offers, ok := commandOffers(appserver.CommandApproval{
			NetworkApprovalContext: &appserver.NetworkContext{Host: "example.com", Protocol: "https"},
			AvailableDecisions:     []json.RawMessage{json.RawMessage(`"accept"`), json.RawMessage(`"acceptForSession"`), c.rule, json.RawMessage(`"decline"`)},
		})
		var ids []string
		for _, o := range offers {
			ids = append(ids, string(o.option.OptionID))
		}
		if got := strings.Join(ids, ","); !ok || got != c.want {
			t.Errorf("%s: %v %s, want %s", c.rule, ok, got, c.want)
		}
	}
	// Without a network request, a network rule isn't offered either.
	offers, ok := commandOffers(appserver.CommandApproval{AvailableDecisions: []json.RawMessage{json.RawMessage(`"accept"`), rule("example.com", "allow"), json.RawMessage(`"cancel"`)}})
	if !ok || len(offers) != 2 {
		t.Errorf("no network: %v %d offers", ok, len(offers))
	}
}
