package codexacp

import (
	"context"
	"errors"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// initialize starts `codex app-server` with the experimental API on, and
// answers with the agent's name and what it can do.
func TestInitialize(t *testing.T) {
	f := codex()
	h := start(t, f)
	resp, err := h.conn.Initialize(t.Context(), &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion,
		ClientInfo: &acp.Implementation{Name: "testclient", Title: "Test Client"}})
	if err != nil {
		t.Fatal(err)
	}
	if spec := f.Spec(); spec.Path != "/fake/codex" || strings.Join(spec.Args, " ") != "app-server" || strings.Join(spec.Env, " ") != "A=1" {
		t.Errorf("started %+v", spec)
	}
	init := f.Calls("initialize")
	if len(init) != 1 || string(init[0]) != `{"clientInfo":{"name":"testclient","title":"Test Client","version":"`+Version+`"},"capabilities":{"experimentalApi":true,"requestAttestation":false}}` {
		t.Errorf("initialize %s", init)
	}
	caps := resp.AgentCapabilities
	if resp.AgentInfo.Name != Name || !caps.LoadSession || !caps.PromptCapabilities.Image || caps.SessionCapabilities.Fork == nil ||
		caps.SessionCapabilities.List == nil || caps.SessionCapabilities.Delete == nil || caps.SessionCapabilities.Close == nil || caps.SessionCapabilities.Resume == nil ||
		!caps.MCPCapabilities.HTTP || caps.MCPCapabilities.SSE {
		t.Errorf("initialize answered %s", jsonOf(t, resp))
	}
	if !strings.Contains(h.logs.String(), "userAgent=fake-codex/0.163.0") {
		t.Errorf("log\n%s", h.logs.String())
	}
}

// A new session is a thread started in its folder, trusted, with the
// client's MCP servers; its options are codex-acp's, its model and
// effort the thread's.
func TestNewSession(t *testing.T) {
	f := codex()
	h := initialized(t, f)
	resp, err := h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj", MCPServers: []acp.MCPServer{
		{Type: "stdio", Stdio: &acp.MCPServerStdio{Name: "my tools", Command: "/bin/tools", Args: []string{"-x"}, Env: []acp.EnvVariable{{Name: "K", Value: "v"}}}},
		{Type: "http", HTTP: &acp.MCPServerHTTP{Name: "web", URL: "http://127.0.0.1:9/mcp", Headers: []acp.HTTPHeader{{Name: "Authorization", Value: "Bearer x"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.SessionID != "thr_1" {
		t.Errorf("session %s", resp.SessionID)
	}
	want := `{"cwd":"/w/proj","config":{"features":{"cwd_relative_turn_diffs":false},` +
		`"mcp_servers":{"my_tools":{"args":["-x"],"command":"/bin/tools","env":{"K":"v"}},"web":{"http_headers":{"Authorization":"Bearer x"},"url":"http://127.0.0.1:9/mcp"}},` +
		`"projects":{"/w/proj":{"trust_level":"trusted"}}}}`
	if got := string(f.Calls("thread/start")[0]); got != want {
		t.Errorf("thread/start\n%s\nwant\n%s", got, want)
	}
	var ids []string
	for _, o := range resp.ConfigOptions {
		ids = append(ids, string(o.ID)+"/"+string(o.Category)+"="+string(o.Select.CurrentValue))
	}
	if got := strings.Join(ids, " "); got != "mode/mode=agent collaboration_mode/collaboration_mode=default model/model=fake-model-1 reasoning_effort/thought_level=medium" {
		t.Errorf("options %s", got)
	}
	if got := jsonOf(t, resp.ConfigOptions[2].Select.Options); got != `[{"value":"fake-model-1","name":"Fake One","description":"Fake One model"},{"value":"fake-model-2","name":"Fake Two","description":"Fake Two model"}]` {
		t.Errorf("models %s", got)
	}
	if got := jsonOf(t, resp.ConfigOptions[3].Select.Options); got != `[{"value":"low","name":"Low","description":"Quick"},{"value":"medium","name":"Medium","description":"Balanced"}]` {
		t.Errorf("efforts %s", got)
	}
	if m := resp.Modes; m.CurrentModeID != "agent" || len(m.AvailableModes) != 4 || m.AvailableModes[3].ID != "agent-full-access" {
		t.Errorf("modes %s", jsonOf(t, m))
	}
}

// Without a login its provider needs, Codex can't start a session: the
// client hears Authentication required, and no thread is started. An
// SSE MCP server is refused, as Codex takes none.
func TestNewSessionRefused(t *testing.T) {
	f := codex()
	f.On("account/read", appservertest.Reply(map[string]any{"account": nil, "requiresOpenaiAuth": true}))
	h := initialized(t, f)
	_, err := h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj"})
	var e *acp.Error
	if !errors.As(err, &e) || e.Code != acp.ErrorCodeAuthenticationRequired {
		t.Errorf("new session: %v", err)
	}
	f.Codex()
	_, err = h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj", MCPServers: []acp.MCPServer{{Type: "sse", SSE: &acp.MCPServerSSE{Name: "s", URL: "http://x"}}}})
	if !errors.As(err, &e) || e.Code != acp.ErrorCodeInvalidRequest {
		t.Errorf("SSE server: %v", err)
	}
	if n := len(f.Calls("thread/start")); n != 0 {
		t.Errorf("%d threads started", n)
	}
}

// Options are the session's: a mode, a model (whose effort stays if it
// has it), an effort it offers; anything else is refused. Close lets go
// of the thread.
func TestSetOptionsAndClose(t *testing.T) {
	f := codex()
	f.On("thread/settings/update", appservertest.Reply(map[string]any{}))
	h := initialized(t, f)
	sid := h.session()
	set := func(id, value string) (string, error) {
		resp, err := h.conn.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: acp.SessionID(sid), ConfigID: acp.SessionConfigID(id),
			ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: acp.SessionConfigValueID(value)}})
		if err != nil {
			return "", err
		}
		var cur []string
		for _, o := range resp.ConfigOptions {
			cur = append(cur, string(o.Select.CurrentValue))
		}
		return strings.Join(cur, " "), nil
	}
	for _, step := range []struct{ id, value, want string }{
		{"mode", "read-only", "read-only default fake-model-1 medium"},
		{"model", "fake-model-2", "read-only default fake-model-2 medium"},
		{"reasoning_effort", "low", "read-only default fake-model-2 low"},
		{"collaboration_mode", "plan", "read-only plan fake-model-2 low"},
	} {
		if got, err := set(step.id, step.value); err != nil || got != step.want {
			t.Errorf("%s=%s: %s, %v; want %s", step.id, step.value, got, err, step.want)
		}
	}
	if got := string(f.Calls("thread/settings/update")[0]); got != `{"collaborationMode":{"mode":"plan","settings":{"model":"fake-model-2","reasoning_effort":"low","developer_instructions":null}},"threadId":"thr_1"}` {
		t.Errorf("settings %s", got)
	}
	for _, bad := range [][2]string{{"mode", "yolo"}, {"model", "nope"}, {"reasoning_effort", "max"}, {"color", "red"}} {
		if _, err := set(bad[0], bad[1]); err == nil {
			t.Errorf("%s=%s taken", bad[0], bad[1])
		}
	}
	if _, err := h.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls("thread/unsubscribe"); len(got) != 1 || string(got[0]) != `{"threadId":"thr_1"}` {
		t.Errorf("unsubscribe %s", got)
	}
	if _, err := set("mode", "agent"); err == nil {
		t.Error("a closed session took an option")
	}
}

// When Codex exits, the agent does: the client's connection ends.
func TestAppServerExit(t *testing.T) {
	f := codex()
	h := initialized(t, f)
	f.Exit()
	<-h.conn.Done()
}

// A session closed mid-turn interrupts its turn before it unsubscribes,
// so Codex doesn't go on working for no one; the prompt ends cancelled,
// as codex-acp ends it (as a run on Codex 0.163.0 showed).
func TestCloseMidTurn(t *testing.T) {
	f := codex()
	f.On("turn/start", appservertest.Reply(map[string]any{"turn": appservertest.TurnOf("turn_1", "inProgress")}))
	f.On("turn/interrupt", appservertest.Reply(map[string]any{}))
	f.On("thread/unsubscribe", appservertest.Reply(map[string]any{"status": "unsubscribed"}))
	h := initialized(t, f)
	sid := h.session()
	type result struct {
		resp *acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := h.conn.Prompt(context.Background(), &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock("go")}})
		done <- result{resp, err}
	}()
	if _, err := f.Await(t.Context(), "turn/start", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range f.All() {
		if c.Method == "turn/interrupt" || c.Method == "thread/unsubscribe" {
			order = append(order, c.Method+" "+string(c.Params))
		}
	}
	if got := strings.Join(order, ", "); got != `turn/interrupt {"threadId":"thr_1","turnId":"turn_1"}, thread/unsubscribe {"threadId":"thr_1"}` {
		t.Errorf("sent %s", got)
	}
	if r := <-done; r.err != nil || r.resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("the prompt of a closed session ended %v, %v", r.resp, r.err)
	}
}
