// Package appservertest is a fake Codex app-server for tests: a script of
// answers by method, notifications and requests sent when a test says,
// and every call the client made kept. Its data is made up, shaped as
// Codex 0.163.0's schema says.
package appservertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/afrase/codex-acp-go/appserver"
)

// Handler answers a call: its result, or an error (an *appserver.Error
// as it is).
type Handler func(params json.RawMessage) (any, error)

// Call is a call or notification the client sent.
type Call struct {
	Method string
	Params json.RawMessage
}

// Fake is a fake app-server. Spawn starts it, once; until then, and after
// Exit, it has no connection.
type Fake struct {
	mu       sync.Mutex
	handlers map[string]Handler
	then     map[string]func(json.RawMessage)
	calls    []Call
	waiters  []waiter
	conn     *appserver.Conn
	spec     appserver.Spec
	stop     func()
	spawned  chan struct{}
	exited   chan struct{}
	exitOnce sync.Once
	exiting  atomic.Bool // set before Exit closes the pipes
}

type waiter struct {
	method string
	seen   int // calls of method before the wait
	ch     chan json.RawMessage
}

// New is a fake whose initialize answers as Codex's does; any other
// method is Method not found until On scripts it.
func New() *Fake {
	f := &Fake{handlers: map[string]Handler{}, then: map[string]func(json.RawMessage){},
		spawned: make(chan struct{}), exited: make(chan struct{})}
	f.On("initialize", Reply(map[string]any{"userAgent": "fake-codex/0.163.0", "codexHome": "/fake/codex-home", "platformFamily": "unix", "platformOs": "macos"}))
	return f
}

// Reply is a Handler that always answers result.
func Reply(result any) Handler {
	return func(json.RawMessage) (any, error) { return result, nil }
}

// Fail is a Handler that always fails with code, message and data (nil:
// none).
func Fail(code int, message string, data any) Handler {
	return func(json.RawMessage) (any, error) {
		e := &appserver.Error{Code: code, Message: message}
		if data != nil {
			e.Data, _ = json.Marshal(data)
		}
		return nil, e
	}
}

// On scripts method's answer.
func (f *Fake) On(method string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

// Then has fn run once a call of method is answered, with its params:
// what the server sends after an answer (a turn's notifications).
func (f *Fake) Then(method string, fn func(params json.RawMessage)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.then[method] = fn
}

// Notify sends a notification to the client; once the fake exits, as a
// server's that has, it goes nowhere.
func (f *Fake) Notify(method string, params any) {
	if err := f.connection().Notify(method, params); err != nil && !f.exiting.Load() {
		panic(fmt.Sprintf("appservertest: notify %s: %v", method, err))
	}
}

// Ask sends a request to the client (an approval, a question) and waits
// for its answer.
func (f *Fake) Ask(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var answer json.RawMessage
	err := f.connection().Call(ctx, method, params, &answer)
	return answer, err
}

// Calls is the params of each call of method so far, in order.
func (f *Fake) Calls(method string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

// All is every call and notification of the client so far, in order.
func (f *Fake) All() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Next waits for the client's next call of method, from the time Next is
// called on, and gives its params; ctx bounds the wait.
func (f *Fake) Next(ctx context.Context, method string) (json.RawMessage, error) {
	return f.Await(ctx, method, len(f.Calls(method)))
}

// Await waits for the client's call of method numbered n (from 0) and
// gives its params.
func (f *Fake) Await(ctx context.Context, method string, n int) (json.RawMessage, error) {
	f.mu.Lock()
	w := waiter{method: method, seen: n, ch: make(chan json.RawMessage, 1)}
	if p := f.nthLocked(method, n); p != nil {
		f.mu.Unlock()
		return p, nil
	}
	f.waiters = append(f.waiters, w)
	f.mu.Unlock()
	select {
	case p := <-w.ch:
		return p, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for %s: %w", method, ctx.Err())
	}
}

func (f *Fake) nthLocked(method string, n int) json.RawMessage {
	for _, c := range f.calls {
		if c.Method == method {
			if n == 0 {
				return c.Params
			}
			n--
		}
	}
	return nil
}

func (f *Fake) record(method string, params json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Method: method, Params: params})
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if p := f.nthLocked(w.method, w.seen); p != nil {
			w.ch <- p
			continue
		}
		kept = append(kept, w)
	}
	f.waiters = kept
}

// Spawn starts the fake as an appserver.Spawner: the client talks to it
// over pipes. Kill (the client's stop) ends it.
func (f *Fake) Spawn(s appserver.Spec) (appserver.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		return appserver.Proc{}, errors.New("appservertest: started twice")
	}
	toServerR, toServerW := io.Pipe()
	fromServerR, fromServerW := io.Pipe()
	f.spec = s
	f.conn = appserver.NewConn(toServerR, fromServerW, server{f}, nil)
	f.stop = func() { _, _ = toServerR.Close(), fromServerW.Close() }
	close(f.spawned)
	return appserver.Proc{In: toServerW, Out: fromServerR, Kill: f.Exit, Said: func() string { return "" }}, nil
}

// Spec is how the client started the fake.
func (f *Fake) Spec() appserver.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec
}

// Exit ends the fake as an app-server that exits: its stdout ends.
func (f *Fake) Exit() {
	f.exitOnce.Do(func() {
		f.exiting.Store(true)
		f.mu.Lock()
		stop := f.stop
		f.mu.Unlock()
		if stop != nil {
			stop()
		}
		close(f.exited)
	})
}

// Exited is closed once the fake has exited, or the client killed it.
func (f *Fake) Exited() <-chan struct{} { return f.exited }

func (f *Fake) connection() *appserver.Conn {
	<-f.spawned
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

// server is the fake's side of the connection.
type server struct{ f *Fake }

func (s server) Notified(method string, params json.RawMessage) { s.f.record(method, params) }

func (s server) Asked(_ context.Context, method string, params json.RawMessage) (any, error) {
	s.f.record(method, params)
	s.f.mu.Lock()
	h := s.f.handlers[method]
	s.f.mu.Unlock()
	if h == nil {
		return nil, &appserver.Error{Code: appserver.CodeMethodNotFound, Message: "fake app-server: no answer for " + method}
	}
	return h(params)
}

func (s server) Replied(method string, params json.RawMessage) {
	s.f.mu.Lock()
	fn := s.f.then[method]
	s.f.mu.Unlock()
	if fn != nil {
		fn(params)
	}
}
