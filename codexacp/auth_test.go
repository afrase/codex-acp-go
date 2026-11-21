package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// signedOut is a Codex with no login until in is set, then signed in to
// ChatGPT; each account/login/start for ChatGPT is login_1, login_2…
// with its page on page(n).
func signedOut() (*appservertest.Fake, *atomic.Bool) {
	f := codex()
	in := new(atomic.Bool)
	f.On("account/read", func(json.RawMessage) (any, error) {
		if in.Load() {
			return map[string]any{"account": map[string]any{"type": "chatgpt", "email": "user@example.com", "planType": "plus"}, "requiresOpenaiAuth": true}, nil
		}
		return map[string]any{"account": nil, "requiresOpenaiAuth": true}, nil
	})
	var mu sync.Mutex
	n := 0
	f.On("account/login/start", func(params json.RawMessage) (any, error) {
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Type != "chatgpt" {
			return map[string]any{"type": p.Type}, nil
		}
		mu.Lock()
		n++
		id := n
		mu.Unlock()
		return map[string]any{"type": "chatgpt", "loginId": "login_" + strconv.Itoa(id), "authUrl": page(id)}, nil
	})
	f.On("account/login/cancel", appservertest.Reply(map[string]any{"status": "canceled"}))
	return f, in
}

func page(n int) string {
	return "https://auth.example.com/oauth/authorize?state=" + strconv.Itoa(n)
}

// completed is account/login/completed's params.
func completed(id string, ok bool) map[string]any {
	var err any
	if !ok {
		err = "Login was not completed"
	}
	return map[string]any{"loginId": id, "success": ok, "error": err, "onboardingEntrypoint": nil}
}

// authenticate runs Authenticate on its own goroutine; its error comes on
// the channel.
func (h *harness) authenticate(ctx context.Context, method string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.conn.Authenticate(ctx, &acp.AuthenticateRequest{MethodID: acp.AuthMethodID(method)})
		done <- err
	}()
	return done
}

// waitOpened waits for the next page the agent opens.
func (h *harness) waitOpened() string {
	h.t.Helper()
	select {
	case p := <-h.opened:
		return p
	case <-time.After(5 * time.Second): // bounds a hang, orders nothing
		h.t.Fatal("no page opened")
		return ""
	}
}

// wait is the error ch gives.
func wait(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second): // bounds a hang, orders nothing
		t.Fatal("authenticate did not return")
		return nil
	}
}

// bounded is a context for a wait on the fake.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second) // bounds a hang, orders nothing
	t.Cleanup(cancel)
	return ctx
}

// authRequired says err is Authentication required with message, from
// method.
func authRequired(t *testing.T, err error, message, method string) {
	t.Helper()
	var e *acp.Error
	if !errors.As(err, &e) || e.Code != acp.ErrorCodeAuthenticationRequired || e.Message != message {
		t.Fatalf("error %v, want Authentication required: %s", err, message)
	}
	if method != "" && string(e.Data) != `{"methodId":"`+method+`"}` {
		t.Errorf("data %s", e.Data)
	}
}

// initialize names codex-acp's auth methods: an API key, and ChatGPT
// unless NO_BROWSER is set.
func TestAuthMethods(t *testing.T) {
	for env, want := range map[string]string{
		"A=1": `[{"id":"api-key","name":"API Key","description":"Use an API key to authenticate","_meta":{"api-key":{"provider":"openai"}}},` +
			`{"id":"chat-gpt","name":"ChatGPT","description":"Use ChatGPT to authenticate"}]`,
		"NO_BROWSER=1": `[{"id":"api-key","name":"API Key","description":"Use an API key to authenticate","_meta":{"api-key":{"provider":"openai"}}}]`,
	} {
		h := start(t, codex(), func(c *Config) { c.Env = []string{env} })
		resp, err := h.conn.Initialize(t.Context(), &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion})
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonOf(t, resp.AuthMethods); got != want {
			t.Errorf("%s: auth methods\n%s\nwant\n%s", env, got, want)
		}
	}
}

// ChatGPT's sign-in opens Codex's page in the browser and ends with the
// login's account/login/completed (another login's is not its end); a
// session then opens.
func TestSignInWithChatGPT(t *testing.T) {
	f, in := signedOut()
	h := initialized(t, f)
	done := h.authenticate(t.Context(), "chat-gpt")
	if got := h.waitOpened(); got != page(1) {
		t.Errorf("opened %s", got)
	}
	if got := string(f.Calls("account/read")[0]); got != `{"refreshToken":true}` {
		t.Errorf("account/read %s", got)
	}
	if got := string(f.Calls("account/login/start")[0]); got != `{"type":"chatgpt"}` {
		t.Errorf("account/login/start %s", got)
	}
	f.Notify("account/login/completed", completed("login_0", false))
	in.Store(true)
	f.Notify("account/login/completed", completed("login_1", true))
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	h.session()
	if n := len(f.Calls("account/login/cancel")); n != 0 {
		t.Errorf("%d cancels", n)
	}
}

// A login that fails is Authentication required, as codex-acp says it;
// sessions still can't open.
func TestSignInFails(t *testing.T) {
	f, _ := signedOut()
	h := initialized(t, f)
	done := h.authenticate(t.Context(), "chat-gpt")
	h.waitOpened()
	f.Notify("account/login/completed", completed("login_1", false))
	authRequired(t, wait(t, done), "Sign-in did not complete", "chat-gpt")
	_, err := h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj"})
	authRequired(t, err, "Authentication required", "")
	if !strings.Contains(h.logs.String(), "Login was not completed") {
		t.Errorf("log\n%s", h.logs.String())
	}
}

// A client that stops waiting has Codex cancel the login.
func TestSignInCancelled(t *testing.T) {
	f, _ := signedOut()
	h := initialized(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	done := h.authenticate(ctx, "chat-gpt")
	h.waitOpened()
	cancel()
	got, err := f.Await(bounded(t), "account/login/cancel", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"loginId":"login_1"}` {
		t.Errorf("account/login/cancel %s", got)
	}
	if err := wait(t, done); err == nil {
		t.Error("a cancelled sign-in succeeded")
	}
}

// A second sign-in ends the first, whose login is cancelled, and signs in
// by its own.
func TestSignInAgain(t *testing.T) {
	f, in := signedOut()
	h := initialized(t, f)
	first := h.authenticate(t.Context(), "chat-gpt")
	h.waitOpened()
	second := h.authenticate(t.Context(), "chat-gpt")
	if got := h.waitOpened(); got != page(2) {
		t.Errorf("opened %s", got)
	}
	authRequired(t, wait(t, first), "Sign-in did not complete", "chat-gpt")
	if got := f.Calls("account/login/cancel"); len(got) != 1 || string(got[0]) != `{"loginId":"login_1"}` {
		t.Errorf("account/login/cancel %s", got)
	}
	in.Store(true)
	f.Notify("account/login/completed", completed("login_2", true))
	if err := wait(t, second); err != nil {
		t.Fatal(err)
	}
}

// A Codex signed in to ChatGPT already stays so: no login, no page.
func TestSignInWithChatGPTAlready(t *testing.T) {
	f, in := signedOut()
	in.Store(true)
	h := initialized(t, f)
	if err := wait(t, h.authenticate(t.Context(), "chat-gpt")); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Calls("account/login/start")); n != 0 || len(h.opened) != 0 {
		t.Errorf("%d logins, %d pages", n, len(h.opened))
	}
}

// A page that can't be opened, or isn't a web page, fails the sign-in
// and cancels its login.
func TestSignInPageNotOpened(t *testing.T) {
	for name, tc := range map[string]struct {
		page string
		open error
	}{
		"open fails": {page(1), errors.New("no browser")},
		"not a page": {"file:///etc/passwd", nil},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := signedOut()
			f.On("account/login/start", appservertest.Reply(map[string]any{"type": "chatgpt", "loginId": "login_1", "authUrl": tc.page}))
			var opened atomic.Bool
			h := initialized(t, f, func(c *Config) {
				c.OpenURL = func(context.Context, string) error { opened.Store(true); return tc.open }
			})
			err := wait(t, h.authenticate(t.Context(), "chat-gpt"))
			var e *acp.Error
			if !errors.As(err, &e) || e.Code != acp.ErrorCodeInternalError || !strings.HasPrefix(e.Message, "Couldn't open the sign-in page") {
				t.Errorf("error %v", err)
			}
			if got := f.Calls("account/login/cancel"); len(got) != 1 || string(got[0]) != `{"loginId":"login_1"}` {
				t.Errorf("account/login/cancel %s", got)
			}
			if opened.Load() != (tc.open != nil) {
				t.Errorf("opened: %v", opened.Load())
			}
		})
	}
}

// A method initialize didn't name is Invalid params.
func TestSignInUnknownMethod(t *testing.T) {
	f, _ := signedOut()
	h := initialized(t, f)
	err := wait(t, h.authenticate(t.Context(), "terminal"))
	var e *acp.Error
	if !errors.As(err, &e) || e.Code != acp.ErrorCodeInvalidParams {
		t.Errorf("error %v", err)
	}
	if n := len(f.Calls("account/login/start")); n != 0 {
		t.Errorf("%d logins", n)
	}
}

// The API key is the request's, else $CODEX_API_KEY's, else
// $OPENAI_API_KEY's; it goes to Codex and never to the log. With none,
// nothing starts.
func TestSignInWithAPIKey(t *testing.T) {
	for name, tc := range map[string]struct {
		env  []string
		meta map[string]any
		want string // the key Codex gets; "" = none
	}{
		"codex var":  {env: []string{"OPENAI_API_KEY=sk-test-other", "CODEX_API_KEY= sk-test-0123456789 "}, want: "sk-test-0123456789"},
		"openai var": {env: []string{"CODEX_API_KEY=", "OPENAI_API_KEY=sk-test-0123456789"}, want: "sk-test-0123456789"},
		"request":    {env: []string{"CODEX_API_KEY=sk-test-other"}, meta: map[string]any{"api-key": map[string]any{"apiKey": "sk-test-0123456789"}}, want: "sk-test-0123456789"},
		"none":       {env: []string{"A=1"}},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := signedOut()
			logs := &logBuffer{}
			h := initialized(t, f, func(c *Config) {
				c.Env = tc.env
				c.Log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			_, err := h.conn.Authenticate(t.Context(), &acp.AuthenticateRequest{MethodID: "api-key", Meta: tc.meta})
			starts := f.Calls("account/login/start")
			if tc.want == "" {
				var e *acp.Error
				if !errors.As(err, &e) || e.Code != acp.ErrorCodeInternalError || e.Message != "CODEX_API_KEY or OPENAI_API_KEY is not set" ||
					string(e.Data) != `{"envVars":["CODEX_API_KEY","OPENAI_API_KEY"]}` {
					t.Errorf("error %v", err)
				}
				if len(starts) != 0 {
					t.Errorf("logins %s", starts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(starts) != 1 || string(starts[0]) != `{"type":"apiKey","apiKey":"`+tc.want+`"}` {
				t.Errorf("account/login/start %s", starts)
			}
			if l := logs.String(); !strings.Contains(l, "signed in") || strings.Contains(l, "sk-test") {
				t.Errorf("log\n%s", l)
			}
		})
	}
}

// An account/read that fails as Codex fails when it can't reach the
// service keeps the ChatGPT login and signs in with no new one; one that
// fails as a login that doesn't work starts a new login.
func TestSignInWhenAccountReadFails(t *testing.T) {
	for _, message := range []string{"workspace routing discovery failed", "workspace routing discovery timed out"} {
		f, _ := signedOut()
		f.On("account/read", appservertest.Fail(-32603, message, nil))
		h := initialized(t, f)
		if err := wait(t, h.authenticate(t.Context(), "chat-gpt")); err != nil {
			t.Errorf("%s: %v", message, err)
		}
		if n := len(f.Calls("account/login/start")); n != 0 {
			t.Errorf("%s: %d logins started", message, n)
		}
	}
	for _, message := range []string{"workspace routing discovery unauthorized (401)", "selected workspace missing from routing discovery",
		"workspace routing requires a ChatGPT account id"} {
		f, _ := signedOut()
		f.On("account/read", appservertest.Fail(-32603, message, nil))
		h := initialized(t, f)
		done := h.authenticate(t.Context(), "chat-gpt")
		if got := h.waitOpened(); got != page(1) {
			t.Errorf("%s: opened %s", message, got)
		}
		f.Notify("account/login/completed", completed("login_1", true))
		if err := wait(t, done); err != nil {
			t.Errorf("%s: %v", message, err)
		}
	}
	f, _ := signedOut()
	f.On("account/read", appservertest.Fail(-32603, "something else", nil))
	h := initialized(t, f)
	var e *acp.Error
	if err := wait(t, h.authenticate(t.Context(), "chat-gpt")); !errors.As(err, &e) || e.Code != acp.ErrorCodeInternalError {
		t.Errorf("another failure: %v", err)
	}
}
