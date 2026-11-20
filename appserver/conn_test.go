package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// server is the other end of a Conn: what the client wrote, line by line,
// and a way to write lines to it.
type server struct {
	lines *bufio.Scanner
	out   io.WriteCloser
}

func (s *server) next(t *testing.T) map[string]any {
	t.Helper()
	if !s.lines.Scan() {
		t.Fatalf("no line from the client: %v", s.lines.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(s.lines.Bytes(), &m); err != nil {
		t.Fatalf("%s: %v", s.lines.Bytes(), err)
	}
	return m
}

func (s *server) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.out, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

// recorder is a Handler that keeps what it got, in order.
type recorder struct {
	mu   sync.Mutex
	got  []string
	seen chan string
	ask  func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

func (r *recorder) Notified(method string, params json.RawMessage) {
	r.mu.Lock()
	r.got = append(r.got, method+" "+string(params))
	r.mu.Unlock()
	r.seen <- method
}

func (r *recorder) Asked(ctx context.Context, method string, params json.RawMessage) (any, error) {
	r.mu.Lock()
	r.got = append(r.got, "ask "+method)
	r.mu.Unlock()
	return r.ask(ctx, method, params)
}

func connect(t *testing.T, h Handler) (*Conn, *server) {
	t.Helper()
	toServerR, toServerW := io.Pipe()
	fromServerR, fromServerW := io.Pipe()
	c := NewConn(fromServerR, toServerW, h, nil)
	t.Cleanup(func() {
		_ = fromServerW.Close()
		_ = toServerR.Close()
		<-c.Done()
	})
	return c, &server{lines: bufio.NewScanner(toServerR), out: fromServerW}
}

func newRecorder() *recorder { return &recorder{seen: make(chan string, 16)} }

// A call is a line without "jsonrpc", answered by its id even when the
// answers come in another order; an error answer is an *Error.
func TestCallsAnsweredByID(t *testing.T) {
	c, s := connect(t, newRecorder())
	type result struct {
		N int `json:"n"`
	}
	errs := make(chan error, 2)
	var one, two result
	go func() { errs <- c.Call(t.Context(), "one", map[string]int{"a": 1}, &one) }()
	first := s.next(t)
	go func() { errs <- c.Call(t.Context(), "two", nil, &two) }()
	second := s.next(t)
	if _, has := first["jsonrpc"]; has || first["method"] != "one" {
		t.Errorf("first line %v", first)
	}
	s.send(t, `{"id":`+jsonOf(t, second["id"])+`,"result":{"n":2}}`)
	s.send(t, `{"id":`+jsonOf(t, first["id"])+`,"error":{"code":-32602,"message":"bad","data":{"details":"why"}}}`)
	var failed *Error
	for range 2 {
		if err := <-errs; err != nil && !errors.As(err, &failed) {
			t.Fatal(err)
		}
	}
	if two.N != 2 || failed == nil || failed.Code != CodeInvalidParams || string(failed.Data) != `{"details":"why"}` {
		t.Errorf("two %v, error %v", two, failed)
	}
}

// The server's notifications reach the handler in order; its request is
// answered with the handler's result, or its error as a JSON-RPC error,
// and starts only after the notifications sent before it were handled.
func TestTheServersMessages(t *testing.T) {
	r := newRecorder()
	r.ask = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "no" {
			return nil, errors.New("refused")
		}
		return map[string]string{"decision": "accept"}, nil
	}
	_, s := connect(t, r)
	s.send(t, `{"method":"a","params":{"x":1}}`)
	s.send(t, `{"method":"b"}`)
	s.send(t, `{"id":7,"method":"ask","params":{}}`)
	if got := s.next(t); jsonOf(t, got) != `{"id":7,"result":{"decision":"accept"}}` {
		t.Errorf("answer %s", jsonOf(t, got))
	}
	s.send(t, `{"id":"s1","method":"no"}`)
	if got := s.next(t); jsonOf(t, got) != `{"error":{"code":-32603,"message":"refused"},"id":"s1"}` {
		t.Errorf("answer %s", jsonOf(t, got))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := strings.Join(r.got, "|"); got != `a {"x":1}|b |ask ask|ask no` {
		t.Errorf("handled %s", got)
	}
}

// A line over MaxLine is skipped and the connection goes on; one that
// isn't JSON too.
func TestLongLinesSkipped(t *testing.T) {
	r := newRecorder()
	_, s := connect(t, r)
	go func() {
		_, _ = io.WriteString(s.out, `{"method":"big","params":"`+strings.Repeat("x", MaxLine)+"\"}\nnot json\n"+`{"method":"after"}`+"\n")
	}()
	select {
	case m := <-r.seen:
		if m != "after" {
			t.Errorf("first notification %s", m)
		}
	case <-time.After(30 * time.Second): // bounds a hang, orders nothing
		t.Fatal("nothing after the long line")
	}
}

// When the server's stdout ends, calls waiting fail with ErrClosed, as do
// later ones, and the request's context ends.
func TestServerExit(t *testing.T) {
	r := newRecorder()
	asked := make(chan context.Context, 1)
	r.ask = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		asked <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c, s := connect(t, r)
	errs := make(chan error, 1)
	go func() { errs <- c.Call(t.Context(), "slow", nil, nil) }()
	s.next(t)
	s.send(t, `{"id":1,"method":"wait"}`)
	ctx := <-asked
	_ = s.out.Close()
	if err := <-errs; !errors.Is(err, ErrClosed) {
		t.Errorf("waiting call: %v", err)
	}
	if err := c.Call(t.Context(), "later", nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("later call: %v", err)
	}
	<-ctx.Done()
	<-c.Done()
}

// A Replier hears of an answer once it's written.
func TestReplied(t *testing.T) {
	r := &replier{recorder: newRecorder(), replied: make(chan string, 1)}
	r.ask = func(context.Context, string, json.RawMessage) (any, error) { return nil, nil } //nolint:nilnil // a null result, as an answer may be
	_, s := connect(t, r)
	s.send(t, `{"id":1,"method":"x"}`)
	if got := jsonOf(t, s.next(t)); got != `{"id":1,"result":null}` {
		t.Errorf("answer %s", got)
	}
	if m := <-r.replied; m != "x" {
		t.Errorf("replied %s", m)
	}
}

type replier struct {
	*recorder
	replied chan string
}

func (r *replier) Replied(method string, _ json.RawMessage) { r.replied <- method }

// The agent built on this package uses only the standard library and
// acp-go: nothing else is required by go.mod, and nothing may creep in.
func TestOnlyStandardLibraryAndACP(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-test", "github.com/afrase/codex-acp-go/...").Output()
	if err != nil {
		t.Fatal(err)
	}
	for p := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		first, _, _ := strings.Cut(p, "/")
		if !strings.Contains(first, ".") || strings.HasPrefix(p, "github.com/afrase/acp-go") ||
			strings.HasPrefix(p, "github.com/afrase/codex-acp-go") {
			continue
		}
		t.Errorf("imports %s", p)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
