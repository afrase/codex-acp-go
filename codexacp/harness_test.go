package codexacp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// client is the test's ACP client: it keeps the agent's updates and
// answers its permission requests by permit.
type client struct {
	acp.UnimplementedClient
	mu      sync.Mutex
	updates []string // each update's JSON
	asked   []*acp.RequestPermissionRequest
	more    chan struct{}
	permit  func(*acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error)
	elicit  func(*acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error)
	forms   []string     // each form asked, as JSON
	log     func(string) // a live test's log of what the client got, if any
}

func (c *client) CreateElicitation(_ context.Context, req *acp.CreateElicitationRequest) (*acp.CreateElicitationResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.logged("? form " + string(b))
	c.mu.Lock()
	c.forms = append(c.forms, string(b))
	elicit := c.elicit
	c.mu.Unlock()
	c.signal()
	if elicit == nil {
		return &acp.CreateElicitationResponse{Action: "cancel", Cancel: true}, nil
	}
	return elicit(req)
}

func (c *client) SessionUpdate(_ context.Context, n *acp.SessionNotification) error {
	b, err := json.Marshal(n.Update)
	if err != nil {
		return err
	}
	c.logged("@ " + string(b))
	c.mu.Lock()
	c.updates = append(c.updates, string(b))
	c.mu.Unlock()
	c.signal()
	return nil
}

func (c *client) RequestPermission(ctx context.Context, req *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
	if c.log != nil {
		b, _ := json.Marshal(req)
		c.log("? permission " + string(b))
	}
	c.mu.Lock()
	c.asked = append(c.asked, req)
	permit := c.permit
	c.mu.Unlock()
	c.signal()
	if permit == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return permit(req)
}

func (c *client) logged(s string) {
	if c.log != nil {
		c.log(s)
	}
}

func (c *client) signal() {
	select {
	case c.more <- struct{}{}:
	default:
	}
}

// harness is the agent served in process on a fake app-server, and a
// client connected to it.
type harness struct {
	t     *testing.T
	fake  *appservertest.Fake
	conn  *acp.AgentConn
	c     *client
	done  chan error
	close func()
	logs  *logBuffer
	// opened is each page the agent opened in a browser: a test's never
	// reach a real one.
	opened chan string
	sent   *sentLog
}

// sentLog is the client's messages the agent has read, by method: a
// write to the pipe returns once the agent has read it all.
type sentLog struct {
	w    io.Writer
	mu   sync.Mutex
	n    map[string]int
	more chan struct{}
}

func (l *sentLog) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	var m struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(p[:n], &m) == nil && m.Method != "" {
		l.mu.Lock()
		l.n[m.Method]++
		l.mu.Unlock()
		select {
		case l.more <- struct{}{}:
		default:
		}
	}
	return n, err
}

// waitSent waits until the agent has read n of the client's messages of
// method.
func (h *harness) waitSent(method string, n int) {
	h.t.Helper()
	deadline := time.After(5 * time.Second) // bounds a hang, orders nothing
	for {
		h.sent.mu.Lock()
		got := h.sent.n[method]
		h.sent.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-h.sent.more:
		case <-deadline:
			h.t.Fatalf("the agent read %d %s, want %d", got, method, n)
		}
	}
}

// logBuffer is the agent's log, as text.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start serves the agent on fake, its Config changed by each of with.
func start(t *testing.T, fake *appservertest.Fake, with ...func(*Config)) *harness {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()
	h := &harness{t: t, fake: fake, c: &client{more: make(chan struct{}, 1)}, done: make(chan error, 1), logs: &logBuffer{}, opened: make(chan string, 8)}
	log := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := Config{CodexPath: "/fake/codex", Env: []string{"A=1"}, Spawn: fake.Spawn, Log: log,
		OpenURL: func(_ context.Context, page string) error { h.opened <- page; return nil }}
	for _, w := range with {
		w(&cfg)
	}
	go func() {
		h.done <- Serve(context.Background(), toAgentR, fromAgentW, cfg)
		_ = fromAgentW.Close()
	}()
	h.sent = &sentLog{w: toAgentW, n: map[string]int{}, more: make(chan struct{}, 1)}
	h.conn = acp.NewAgentConn(fromAgentR, h.sent, func(*acp.AgentConn) acp.Client { return h.c })
	h.close = func() { _ = toAgentW.Close() }
	t.Cleanup(func() {
		h.close()
		if err := <-h.done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return h
}

// initialized is a harness whose client has initialized the agent, and
// takes forms.
func initialized(t *testing.T, fake *appservertest.Fake, with ...func(*Config)) *harness {
	t.Helper()
	h := start(t, fake, with...)
	_, err := h.conn.Initialize(t.Context(), &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion,
		ClientCapabilities: &acp.ClientCapabilities{Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}},
		ClientInfo:         &acp.Implementation{Name: "testclient", Title: "Test Client"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// session opens a session in /w/proj on a scripted Codex.
func (h *harness) session() string {
	h.t.Helper()
	resp, err := h.conn.NewSession(h.t.Context(), &acp.NewSessionRequest{Cwd: "/w/proj", MCPServers: []acp.MCPServer{}})
	if err != nil {
		h.t.Fatal(err)
	}
	return string(resp.SessionID)
}

// updates is the updates so far, one JSON a line.
func (h *harness) updates() string {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return strings.Join(h.c.updates, "\n")
}

// waitUpdates waits until there are n updates, and gives them.
func (h *harness) waitUpdates(n int) string {
	h.t.Helper()
	deadline := time.After(5 * time.Second) // bounds a hang, orders nothing
	for {
		h.c.mu.Lock()
		got := len(h.c.updates)
		h.c.mu.Unlock()
		if got >= n {
			return h.updates()
		}
		select {
		case <-h.c.more:
		case <-deadline:
			h.t.Fatalf("%d updates, want %d:\n%s", got, n, h.updates())
		}
	}
}

// waitAsked waits for the client's nth permission request (from 1).
func (h *harness) waitAsked(n int) *acp.RequestPermissionRequest {
	h.t.Helper()
	deadline := time.After(5 * time.Second) // bounds a hang, orders nothing
	for {
		h.c.mu.Lock()
		if len(h.c.asked) >= n {
			r := h.c.asked[n-1]
			h.c.mu.Unlock()
			return r
		}
		h.c.mu.Unlock()
		select {
		case <-h.c.more:
		case <-deadline:
			h.t.Fatalf("no permission request %d", n)
		}
	}
}

// codex is a fake scripted as a signed-in Codex.
func codex() *appservertest.Fake {
	f := appservertest.New()
	f.Codex()
	return f
}

// jsonOf is v as JSON, or the test fails.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
