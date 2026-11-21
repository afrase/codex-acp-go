package codexacp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// The live tests drive the agent on a real Codex. They run only with
// CODEX_ACP_LIVE=1 and CODEX_ACP_LIVE_HOME set to a CODEX_HOME that can
// answer a prompt (signed in, or with a model provider in its
// config.toml). Each test copies that home's config.toml, auth.json and
// installation_id to a temp folder, so the original is never written. The
// codex run is $CODEX_PATH, else codex on PATH. CODEX_ACP_LIVE_LOGS, when
// set, is a folder for each test's traffic: the app-server's lines and
// the ACP updates, never the environment.
//
// They talk to a model, so they are slow, cost tokens and depend on what
// it chooses to do: they check what the agent sends, and log the rest.

// liveEnv is the live test's settings, or the test is skipped.
func liveEnv(t *testing.T) (codex, home string) {
	t.Helper()
	home = os.Getenv("CODEX_ACP_LIVE_HOME")
	if os.Getenv("CODEX_ACP_LIVE") != "1" || home == "" {
		t.Skip("CODEX_ACP_LIVE=1 and CODEX_ACP_LIVE_HOME (a CODEX_HOME) run it against a real Codex")
	}
	codex = os.Getenv("CODEX_PATH")
	if codex == "" {
		codex = "codex"
	}
	return codex, home
}

// lived is a live test's agent and where it works: its CODEX_HOME and a
// git repository as the session's folder.
type lived struct {
	*harness
	home, repo string
	wire       *wireLog
}

// liveStart serves the agent on the real Codex, in a copy of the home.
func liveStart(t *testing.T) *lived {
	t.Helper()
	codex, base := liveEnv(t)
	dir := t.TempDir()
	home, repo := filepath.Join(dir, "home"), filepath.Join(dir, "repo")
	copyHome(t, base, home)
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "config", "user.email", "t@example.com"}, {"-C", repo, "config", "user.name", "t"}} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(repo, "hello.txt"), "hello\n")
	writeFile(t, filepath.Join(repo, "notes.md"), "# notes\n")
	repo, _ = filepath.EvalSymlinks(repo)
	env := append(os.Environ(), "CODEX_HOME="+home)
	l := &lived{home: home, repo: repo, wire: &wireLog{}}
	l.harness = serveLive(t, Config{CodexPath: codex, Env: env, Spawn: l.wire.spawn}, l.wire)
	t.Cleanup(func() { l.wire.save(t) })
	return l
}

func serveLive(t *testing.T, cfg Config, wire *wireLog) *harness {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()
	h := &harness{t: t, c: &client{more: make(chan struct{}, 1)}, done: make(chan error, 1)}
	h.c.log = wire.line
	go func() {
		h.done <- Serve(context.Background(), toAgentR, fromAgentW, cfg)
		_ = fromAgentW.Close()
	}()
	h.conn = acp.NewAgentConn(fromAgentR, toAgentW, func(*acp.AgentConn) acp.Client { return h.c })
	h.close = func() { _ = toAgentW.Close() }
	t.Cleanup(func() {
		h.close()
		if err := <-h.done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	_, err := h.conn.Initialize(t.Context(), &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion,
		ClientCapabilities: &acp.ClientCapabilities{Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}},
		ClientInfo:         &acp.Implementation{Name: "codexacp-live-test", Title: "codexacp live test"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// copyHome copies a CODEX_HOME's config and auth, not its sessions or
// databases.
func copyHome(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.toml", "auth.json", "installation_id"} {
		b, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			continue
		}
		writeFile(t, filepath.Join(to, name), string(b))
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// open starts a session in the repository, in mode.
func (l *lived) open(mode string) string {
	l.t.Helper()
	resp, err := l.conn.NewSession(l.t.Context(), &acp.NewSessionRequest{Cwd: l.repo, MCPServers: []acp.MCPServer{}})
	if err != nil {
		l.t.Fatal(err)
	}
	l.wire.line("= new " + jsonOf(l.t, resp))
	if mode != "" {
		if _, err := l.conn.SetSessionMode(l.t.Context(), &acp.SetSessionModeRequest{SessionID: resp.SessionID, ModeID: acp.SessionModeID(mode)}); err != nil {
			l.t.Fatal(err)
		}
	}
	return string(resp.SessionID)
}

// prompt runs a prompt to its end.
func (l *lived) prompt(sid, text string) *acp.PromptResponse {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), 3*time.Minute)
	defer cancel()
	resp, err := l.conn.Prompt(ctx, &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock(text)}})
	if err != nil {
		l.t.Fatalf("prompt %q: %v", text, err)
	}
	l.wire.line("= prompt " + jsonOf(l.t, resp))
	return resp
}

// wireLog keeps, in order, the lines the agent and the app-server sent
// each other and the ACP updates the client got.
type wireLog struct {
	mu    sync.Mutex
	lines []string
	start time.Time
}

func (w *wireLog) line(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.start.IsZero() {
		w.start = time.Now()
	}
	w.lines = append(w.lines, time.Since(w.start).Round(time.Millisecond).String()+" "+s)
}

// spawn starts the app-server as appserver.Exec does, its lines logged.
func (w *wireLog) spawn(s appserver.Spec) (appserver.Proc, error) {
	p, err := appserver.Exec(s)
	if err != nil {
		return p, err
	}
	in := &lineSplitter{emit: func(b []byte) { w.line("> " + string(b)) }}
	p.In = writeCloser{io.MultiWriter(p.In, in), p.In}
	p.Out = io.TeeReader(p.Out, &lineSplitter{emit: func(b []byte) { w.line("< " + string(b)) }})
	return p, nil
}

func (w *wireLog) save(t *testing.T) {
	t.Helper()
	dir := os.Getenv("CODEX_ACP_LIVE_LOGS")
	if dir == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	name := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_")+".log")
	if err := os.WriteFile(name, []byte(strings.Join(w.lines, "\n")+"\n"), 0o600); err != nil {
		t.Error(err)
	}
}

type writeCloser struct {
	io.Writer
	io.Closer
}

// lineSplitter hands on each whole line written to it.
type lineSplitter struct {
	buf  []byte
	emit func([]byte)
}

func (l *lineSplitter) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		l.emit(bytes.Clone(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
}

// TestLivePrompt: a new session's modes and options, and a plain prompt's
// text, ids, usage and stop reason.
func TestLivePrompt(t *testing.T) {
	l := liveStart(t)
	sid := l.open("")
	resp := l.prompt(sid, "Reply with exactly: hi there")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stop %s", resp.StopReason)
	}
}

// touchPrompt has the model ask for approval up front: a model may
// otherwise run it in the sandbox, fail and give up.
const touchPrompt = "Run exactly `touch made.txt` with your shell tool, asking for escalated sandbox permissions (sandbox_permissions: require_escalated) on the first call. If it is refused, don't retry: say refused."

// TestLiveCommand: a command the model runs, in read-only and workspace
// mode, and a write in read-only, asked and allowed once or refused.
func TestLiveCommand(t *testing.T) {
	for _, c := range []struct {
		name, mode, prompt, pick string
		made                     bool // made.txt is there after
	}{
		{"ls-read-only", "read-only", "Use your shell tool to run `ls` in the current folder, then tell me what's there.", "", false},
		{"ls-workspace", "workspace-write", "Use your shell tool to run `ls` in the current folder, then tell me what's there.", "", false},
		{"touch-allowed", "read-only", touchPrompt, "allow_once", true},
		{"touch-refused", "read-only", touchPrompt, "cancel", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := liveStart(t)
			if c.pick != "" {
				l.c.permit = pick(c.pick)
			}
			sid := l.open(c.mode)
			l.prompt(sid, c.prompt)
			_, err := os.Stat(filepath.Join(l.repo, "made.txt"))
			if made := err == nil; made != c.made {
				t.Errorf("made.txt there: %v", made)
			}
		})
	}
}

// TestLiveEdit: an edit the model makes, asked in read-only and allowed,
// shown as a diff.
func TestLiveEdit(t *testing.T) {
	l := liveStart(t)
	l.c.permit = pick("allow_once")
	sid := l.open("read-only")
	l.prompt(sid, "Edit hello.txt with your apply_patch tool so that its second line is `world`. Don't use the shell. Then say done.")
	if b, _ := os.ReadFile(filepath.Join(l.repo, "hello.txt")); string(b) != "hello\nworld\n" {
		t.Errorf("hello.txt is %q", b)
	}
	l.reload(sid)
}

// reload closes the session and loads it again, the replay logged.
func (l *lived) reload(sid string) {
	l.t.Helper()
	if _, err := l.conn.CloseSession(l.t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		l.t.Fatal(err)
	}
	l.wire.line("= reload")
	if _, err := l.conn.LoadSession(l.t.Context(), &acp.LoadSessionRequest{SessionID: acp.SessionID(sid), Cwd: l.repo, MCPServers: []acp.MCPServer{}}); err != nil {
		l.t.Fatal(err)
	}
	l.wire.line("= loaded")
}

// promptAsync starts a prompt and gives its result once it ends.
func (l *lived) promptAsync(sid, text string) <-chan *acp.PromptResponse {
	done := make(chan *acp.PromptResponse, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		resp, err := l.conn.Prompt(ctx, &acp.PromptRequest{SessionID: acp.SessionID(sid), Prompt: []acp.ContentBlock{textBlock(text)}})
		if err != nil {
			l.wire.line("= prompt failed " + err.Error())
		} else {
			l.wire.line("= prompt " + jsonOf(l.t, resp))
		}
		done <- resp
	}()
	return done
}

// waitFor waits until an update has want in its JSON.
func (l *lived) waitFor(want string) {
	l.t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		if strings.Contains(l.updates(), want) {
			return
		}
		select {
		case <-l.c.more:
		case <-deadline:
			l.t.Fatalf("no update with %s", want)
		}
	}
}

// TestLiveCancel: a cancel while a command runs ends the prompt cancelled,
// and Codex's turn ends interrupted.
func TestLiveCancel(t *testing.T) {
	l := liveStart(t)
	sid := l.open("agent-full-access")
	done := l.promptAsync(sid, "Run exactly `sleep 30` with your shell tool (one call, wait for it to finish), then say finished.")
	l.waitFor(`sleep 30`)
	if err := l.conn.Cancel(t.Context(), &acp.CancelNotification{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if resp := <-done; resp == nil || resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("prompt ended %v", resp)
	}
	// The command itself isn't stopped by the interrupt: its call ends
	// when it does.
	l.waitFor(`"terminal_exit"`)
}

// TestLiveReload: a session closed and loaded replays what streamed live,
// takes another prompt, and then resumes, forks, lists and deletes.
func TestLiveReload(t *testing.T) {
	l := liveStart(t)
	sid := l.open("read-only")
	l.prompt(sid, "Use your shell tool to run `ls`, then tell me in one line what's there.")
	live := l.updates()
	if _, err := l.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	l.wire.line("= live\n" + live)
	n := len(l.c.updates)
	load, err := l.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: acp.SessionID(sid), Cwd: l.repo, MCPServers: []acp.MCPServer{}})
	if err != nil {
		t.Fatal(err)
	}
	l.wire.line("= load " + jsonOf(t, load))
	l.c.mu.Lock()
	replay := strings.Join(l.c.updates[n:], "\n")
	l.c.mu.Unlock()
	l.wire.line("= replay\n" + replay)
	l.prompt(sid, "Reply with exactly: second")

	if _, err := l.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	resume, err := l.conn.ResumeSession(t.Context(), &acp.ResumeSessionRequest{SessionID: acp.SessionID(sid), Cwd: l.repo, MCPServers: []acp.MCPServer{}})
	if err != nil {
		t.Fatal(err)
	}
	l.wire.line("= resume " + jsonOf(t, resume))
	fork, err := l.conn.ForkSession(t.Context(), &acp.ForkSessionRequest{SessionID: acp.SessionID(sid), Cwd: l.repo, MCPServers: []acp.MCPServer{}})
	if err != nil {
		t.Fatal(err)
	}
	l.wire.line("= fork " + jsonOf(t, fork))
	l.prompt(string(fork.SessionID), "Reply with exactly: forked")
	list, err := l.conn.ListSessions(t.Context(), &acp.ListSessionsRequest{Cwd: l.repo})
	if err != nil {
		t.Fatal(err)
	}
	l.wire.line("= list " + jsonOf(t, list))
	if len(list.Sessions) != 2 {
		t.Errorf("listed %d sessions, want 2", len(list.Sessions))
	}
	if _, err := l.conn.DeleteSession(t.Context(), &acp.DeleteSessionRequest{SessionID: fork.SessionID}); err != nil {
		t.Fatal(err)
	}
	list, err = l.conn.ListSessions(t.Context(), &acp.ListSessionsRequest{Cwd: l.repo})
	if err != nil {
		t.Fatal(err)
	}
	l.wire.line("= list after delete " + jsonOf(t, list))
	if len(list.Sessions) != 1 || string(list.Sessions[0].SessionID) != sid {
		t.Errorf("after delete, listed %s", jsonOf(t, list.Sessions))
	}
}

// TestLiveSteer: a message steered into a running turn reaches it.
func TestLiveSteer(t *testing.T) {
	l := liveStart(t)
	sid := l.open("agent-full-access")
	done := l.promptAsync(sid, "Run exactly `sleep 8` with your shell tool and wait for it. Then reply with the word apple, plus anything I add meanwhile.")
	l.waitFor(`sleep 8`)
	out, err := steering.Steer(t.Context(), l.conn, sid, []acp.ContentBlock{textBlock("Also end your reply with the word banana.")})
	if err != nil || out != steering.Injected {
		t.Fatalf("steer: %v, %v", out, err)
	}
	if resp := <-done; resp == nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("prompt ended %v", resp)
	}
	l.wire.line("= steered " + string(out))
}

// TestLiveWithdraw: a cancel while a permission is asked withdraws it
// (Codex hears cancel) and the prompt ends cancelled.
func TestLiveWithdraw(t *testing.T) {
	l := liveStart(t)
	sid := l.open("read-only")
	done := l.promptAsync(sid, touchPrompt)
	l.waitFor(`touch made.txt`)
	l.waitAsked(1)
	if err := l.conn.Cancel(t.Context(), &acp.CancelNotification{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if resp := <-done; resp == nil || resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("prompt ended %v", resp)
	}
	time.Sleep(time.Second) // for what Codex sends after the turn, to be logged
}

// TestLiveQuestion: in plan mode, the model's question is a form; one is
// answered, and a second, asked again, is withdrawn by a cancel.
func TestLiveQuestion(t *testing.T) {
	l := liveStart(t)
	l.c.elicit = func(req *acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error) {
		var schema struct {
			Properties map[string]struct {
				OneOf []struct {
					Const string `json:"const"`
				} `json:"oneOf"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(jsonOf(t, req.Form.RequestedSchema)), &schema); err != nil {
			return nil, err
		}
		values := map[string]acp.ElicitationContentValue{}
		for name, p := range schema.Properties {
			if len(p.OneOf) > 0 { // the first choice; a note is left out
				v := p.OneOf[0].Const
				values[name] = acp.ElicitationContentValue{String: &v}
			}
		}
		return &acp.CreateElicitationResponse{Action: "accept", Accept: &acp.ElicitationAcceptAction{Content: values}}, nil
	}
	sid := l.open("")
	if _, err := l.conn.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: acp.SessionID(sid), ConfigID: optCollab,
		ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: "plan"}}); err != nil {
		t.Fatal(err)
	}
	l.prompt(sid, "Before planning anything, use your request_user_input tool to ask me one multiple-choice question: which color I like (red or blue). Then reply with the answer only.")
}

// TestLiveQuestionWithdrawn: a cancel while a plan-mode question is asked
// withdraws it and ends the prompt cancelled.
func TestLiveQuestionWithdrawn(t *testing.T) {
	l := liveStart(t)
	l.c.elicit = func(*acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error) {
		<-l.t.Context().Done() // never answered: the cancel withdraws it
		return nil, l.t.Context().Err()
	}
	sid := l.open("")
	if _, err := l.conn.SetSessionConfigOption(t.Context(), &acp.SetSessionConfigOptionRequest{SessionID: acp.SessionID(sid), ConfigID: optCollab,
		ValueID: &acp.SetSessionConfigOptionRequestValueID{Value: "plan"}}); err != nil {
		t.Fatal(err)
	}
	done := l.promptAsync(sid, "Before planning anything, use your request_user_input tool to ask me one multiple-choice question: which color I like (red or blue). Then reply with the answer only.")
	l.waitForm()
	if err := l.conn.Cancel(t.Context(), &acp.CancelNotification{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if resp := <-done; resp == nil || resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("prompt ended %v", resp)
	}
	time.Sleep(time.Second) // for what Codex sends after the turn, to be logged
}

// waitForm waits for the client's first form.
func (l *lived) waitForm() {
	l.t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		l.c.mu.Lock()
		n := len(l.c.forms)
		l.c.mu.Unlock()
		if n > 0 {
			return
		}
		select {
		case <-l.c.more:
		case <-deadline:
			l.t.Fatal("no form asked")
		}
	}
}

// TestLiveNetwork: a command that needs the network, in workspace mode.
func TestLiveNetwork(t *testing.T) {
	l := liveStart(t)
	l.c.permit = pick("cancel")
	sid := l.open("workspace-write")
	l.prompt(sid, "Run exactly `curl -sI https://example.com` with your shell tool, once. Don't retry; report what happened in one line.")
}

// TestLiveCloseMidTurn: a session closed while its turn runs interrupts
// it, loads again and takes a prompt; a session never prompted loads too.
func TestLiveCloseMidTurn(t *testing.T) {
	l := liveStart(t)
	empty := l.open("")
	if _, err := l.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(empty)}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: acp.SessionID(empty), Cwd: l.repo, MCPServers: []acp.MCPServer{}}); err != nil {
		t.Errorf("load of a session never prompted: %v", err)
	}
	sid := l.open("agent-full-access")
	done := l.promptAsync(sid, "Run exactly `sleep 6` with your shell tool and wait for it, then say finished.")
	l.waitFor(`sleep 6`)
	if _, err := l.conn.CloseSession(t.Context(), &acp.CloseSessionRequest{SessionID: acp.SessionID(sid)}); err != nil {
		t.Fatal(err)
	}
	if resp := <-done; resp == nil || resp.StopReason != acp.StopReasonCancelled {
		t.Errorf("a closed session's prompt ended %v", resp)
	}
	if _, err := l.conn.LoadSession(t.Context(), &acp.LoadSessionRequest{SessionID: acp.SessionID(sid), Cwd: l.repo, MCPServers: []acp.MCPServer{}}); err != nil {
		t.Fatal(err)
	}
	l.prompt(sid, "Reply with exactly: again")
}

// A Codex with no login refuses a session, and its ChatGPT sign-in gives
// a page to open; cancelling the request leaves the agent answering.
func TestLiveSignInOpensAPage(t *testing.T) {
	codex, _ := liveEnv(t)
	home := t.TempDir() // no config and no login
	opened := make(chan string, 1)
	wire := &wireLog{}
	h := serveLive(t, Config{CodexPath: codex, Env: append(os.Environ(), "CODEX_HOME="+home), Spawn: wire.spawn,
		OpenURL: func(_ context.Context, page string) error { opened <- page; return nil }}, wire)
	t.Cleanup(func() { wire.save(t) })
	refused := func() {
		t.Helper()
		_, err := h.conn.NewSession(t.Context(), &acp.NewSessionRequest{Cwd: t.TempDir()})
		if e, ok := errors.AsType[*acp.Error](err); !ok || e.Code != acp.ErrorCodeAuthenticationRequired {
			t.Fatalf("session/new without a login: %v", err)
		}
	}
	refused()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.conn.Authenticate(ctx, &acp.AuthenticateRequest{MethodID: authChatGPT})
		done <- err
	}()
	select {
	case page := <-opened:
		u, err := url.Parse(page)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			t.Fatalf("the sign-in page isn't an https page (host %q): %v", u.Host, err)
		}
		t.Logf("sign-in page on %s", u.Host)
	case err := <-done:
		t.Fatalf("authenticate ended with no page: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("no sign-in page within a minute")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled sign-in succeeded")
	}
	refused()
}
