package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// Sign-in, as codex-acp offers it: initialize names the methods, and the
// client's authenticate has Codex sign in by one. The login is Codex's
// own: the agent starts it and waits for its end, and keeps nothing.

// The auth methods' ids, as codex-acp's.
const (
	authAPIKey  = "api-key"
	authChatGPT = "chat-gpt"
)

// The variables an API key is read from, in order, as codex-acp reads
// them.
var apiKeyVars = []string{"CODEX_API_KEY", "OPENAI_API_KEY"}

// cancelGrace bounds account/login/cancel, sent once the client has
// stopped waiting.
const cancelGrace = 5 * time.Second

// authMethods is what initialize offers, as codex-acp does: an API key
// from the environment, and ChatGPT in a browser unless NO_BROWSER is
// set.
func (a *agent) authMethods() []acp.AuthMethod {
	methods := []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{
		ID: authAPIKey, Name: "API Key", Description: "Use an API key to authenticate",
		Meta: map[string]any{"api-key": map[string]any{"provider": "openai"}},
	}}}
	if a.getenv("NO_BROWSER") == "" {
		methods = append(methods, acp.AuthMethod{Agent: &acp.AuthMethodAgent{
			ID: authChatGPT, Name: "ChatGPT", Description: "Use ChatGPT to authenticate",
		}})
	}
	return methods
}

// Authenticate signs Codex in by the method the client picked. A sign-in
// begun while another waits ends that one: the user has started over.
func (a *agent) Authenticate(ctx context.Context, req *acp.AuthenticateRequest) (*acp.AuthenticateResponse, error) {
	si := a.beginSignIn()
	defer a.endSignIn(si)
	var (
		ok  bool
		err error
	)
	switch req.MethodID {
	case authAPIKey:
		err = a.signInWithKey(ctx, req.Meta)
		ok = err == nil
	case authChatGPT:
		ok, err = a.signInWithChatGPT(ctx, si)
	default:
		return nil, invalidParams("no auth method %q", req.MethodID)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		a.log.Info("sign-in did not complete", "method", req.MethodID)
		return nil, &acp.Error{Code: acp.ErrorCodeAuthenticationRequired, Message: "Sign-in did not complete",
			Data: raw(map[string]any{"methodId": req.MethodID})}
	}
	a.log.Info("signed in", "method", req.MethodID)
	return &acp.AuthenticateResponse{}, nil
}

// signInWithKey signs in with an API key: the request's
// _meta["api-key"].apiKey, else the first of $CODEX_API_KEY and
// $OPENAI_API_KEY set.
func (a *agent) signInWithKey(ctx context.Context, meta map[string]any) error {
	key := keyIn(meta)
	for _, v := range apiKeyVars {
		if key != "" {
			break
		}
		key = strings.TrimSpace(a.getenv(v))
	}
	if key == "" {
		return &acp.Error{Code: acp.ErrorCodeInternalError, Message: strings.Join(apiKeyVars, " or ") + " is not set",
			Data: raw(map[string]any{"envVars": apiKeyVars})}
	}
	// Codex has the key once it answers: the login has no id, and nothing
	// to wait for.
	return a.call(ctx, "account/login/start", appserver.LoginStartParams{Type: "apiKey", APIKey: key}, nil)
}

// keyIn is the API key a request's _meta carries, or "".
func keyIn(meta map[string]any) string {
	m, _ := meta["api-key"].(map[string]any)
	key, _ := m["apiKey"].(string)
	return key
}

// signInWithChatGPT has Codex sign in to ChatGPT: Codex serves the
// sign-in's callback and gives the page to open, the agent opens it in
// the user's browser and waits for account/login/completed. A Codex
// already signed in to ChatGPT stays so. A client that stops waiting
// cancels the login.
func (a *agent) signInWithChatGPT(ctx context.Context, si *signIn) (bool, error) {
	if ok, err := a.hasChatGPT(ctx); err != nil || ok {
		return ok, err
	}
	if si.over() { // its login would replace the newer sign-in's
		return false, nil
	}
	var started appserver.LoginStartResponse
	if err := a.call(ctx, "account/login/start", appserver.LoginStartParams{Type: "chatgpt"}, &started); err != nil {
		return false, err
	}
	if started.Type != "chatgpt" || started.LoginID == "" {
		return false, nil
	}
	si.started(started.LoginID)
	if si.over() {
		a.cancelLogin(ctx, started.LoginID)
		return false, nil
	}
	if err := a.openURL(ctx, started.AuthURL); err != nil {
		a.cancelLogin(ctx, started.LoginID)
		return false, &acp.Error{Code: acp.ErrorCodeInternalError, Message: "Couldn't open the sign-in page: " + err.Error()}
	}
	a.log.Info("sign-in page opened", "method", authChatGPT)
	select {
	case end := <-si.done:
		if !end.Success && end.Error != nil {
			a.log.Info("Codex's sign-in failed", "err", *end.Error)
		}
		return end.Success, nil
	case <-si.superseded:
		a.cancelLogin(ctx, started.LoginID)
		return false, nil
	case <-ctx.Done():
		a.cancelLogin(ctx, started.LoginID)
		return false, ctx.Err()
	}
}

// The account/read failures codex-acp reads as an answer about a ChatGPT
// login: Codex couldn't reach the service (the login stays), or found
// that the login doesn't work (a new one replaces it).
var (
	readUnavailable = map[string]bool{
		"workspace routing discovery failed":    true,
		"workspace routing discovery timed out": true,
	}
	readAuthFailure = map[string]bool{
		"workspace routing discovery unauthorized (401)":    true,
		"selected workspace missing from routing discovery": true,
		"workspace routing requires a ChatGPT account id":   true,
	}
)

// hasChatGPT says Codex has a ChatGPT login that works, refreshing its
// token, as codex-acp checks before a new login.
func (a *agent) hasChatGPT(ctx context.Context) (bool, error) {
	var acct appserver.AccountReadResponse
	err := a.callServer(ctx, "account/read", appserver.AccountReadParams{RefreshToken: true}, &acct)
	if e, ok := errors.AsType[*appserver.Error](err); ok && e.Code == appserver.CodeInternal {
		switch {
		case readUnavailable[e.Message]:
			a.log.Info("Codex can't check its ChatGPT login, so it stays", "err", e.Message)
			return true, nil
		case readAuthFailure[e.Message]:
			a.log.Info("Codex's ChatGPT login doesn't work, so a new one starts", "err", e.Message)
			return false, nil
		}
	}
	if err != nil {
		return false, clientError(err)
	}
	return acct.AccountType() == "chatgpt", nil
}

// openURL opens a sign-in page in the user's browser: a web page only,
// never a file or another program's link.
func (a *agent) openURL(ctx context.Context, page string) error {
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("the page Codex gave isn't a web page")
	}
	open := a.cfg.OpenURL
	if open == nil {
		open = openBrowser
	}
	return open(ctx, page)
}

// openBrowser opens page in the user's default browser. The opener
// outlives the request, and runs detached from the agent's process group
// (on Windows, its job object), so a client that ends that group once
// signed in doesn't end the browser the opener may have become. A job
// that doesn't let it leave has it run in the job.
func openBrowser(ctx context.Context, page string) error {
	err := launch(browserOpener(ctx, page))
	if detachRefused(err) {
		err = launch(browserCommand(ctx, page))
	}
	return err
}

// browserOpener is the command openBrowser starts.
func browserOpener(ctx context.Context, page string) *exec.Cmd {
	return detach(browserCommand(ctx, page))
}

// launch starts a program that opens a page, and doesn't wait for it.
func launch(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// cancelLogin has Codex stop a login, even once ctx has ended; one that
// has ended already is Codex's to ignore.
func (a *agent) cancelLogin(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelGrace)
	defer cancel()
	if err := a.call(ctx, "account/login/cancel", appserver.LoginCancelParams{LoginID: id}, nil); err != nil {
		a.log.Debug("login not cancelled", "err", err)
	}
}

// getenv is a variable of Codex's environment: Config.Env, or this
// process's.
func (a *agent) getenv(key string) string {
	if a.cfg.Env == nil {
		return os.Getenv(key)
	}
	var v string
	for _, kv := range a.cfg.Env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val // the last one counts, as for a process
		}
	}
	return v
}

// signIn is an Authenticate under way: the login it waits for, once
// Codex has given its id.
type signIn struct {
	done       chan appserver.LoginCompleted // the login's end; holds one
	superseded chan struct{}                 // closed once another sign-in begins

	mu    sync.Mutex
	id    string                     // "" until account/login/start answers
	early []appserver.LoginCompleted // ends heard before then
}

// beginSignIn makes si the sign-in account/login/completed goes to, and
// ends the one before it.
func (a *agent) beginSignIn() *signIn {
	si := &signIn{done: make(chan appserver.LoginCompleted, 1), superseded: make(chan struct{})}
	a.mu.Lock()
	old := a.signin
	a.signin = si
	a.mu.Unlock()
	if old != nil {
		close(old.superseded)
	}
	return si
}

// over says another sign-in has begun since si.
func (si *signIn) over() bool {
	select {
	case <-si.superseded:
		return true
	default:
		return false
	}
}

// endSignIn lets go of si, unless another has taken its place.
func (a *agent) endSignIn(si *signIn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.signin == si {
		a.signin = nil
	}
}

// loginCompleted hands account/login/completed to the sign-in waiting.
func (a *agent) loginCompleted(params json.RawMessage) {
	var end appserver.LoginCompleted
	if err := json.Unmarshal(params, &end); err != nil {
		a.log.Warn("a Codex message doesn't decode", "method", "account/login/completed", "err", err)
		return
	}
	a.mu.Lock()
	si := a.signin
	a.mu.Unlock()
	if si != nil {
		si.completed(end)
	}
}

// started notes the login's id: an end of it heard already is its end.
func (si *signIn) started(id string) {
	si.mu.Lock()
	defer si.mu.Unlock()
	si.id = id
	for _, end := range si.early {
		si.finish(end)
	}
	si.early = nil
}

// completed takes a login's end: si's own, or one heard before si knew
// its id; another login's is dropped.
func (si *signIn) completed(end appserver.LoginCompleted) {
	si.mu.Lock()
	defer si.mu.Unlock()
	if si.id == "" {
		si.early = append(si.early, end)
		return
	}
	si.finish(end)
}

// finish ends si with end if it is its login's; si.mu is held.
func (si *signIn) finish(end appserver.LoginCompleted) {
	if end.LoginID == nil || *end.LoginID != si.id {
		return
	}
	select {
	case si.done <- end:
	default: // ended already
	}
}
