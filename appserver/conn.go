// Package appserver is a client of Codex's app-server (`codex app-server`),
// the JSON-RPC protocol that Codex's own editor extensions and codex-acp
// speak to it: one JSON object a line on the program's stdin and stdout,
// with no "jsonrpc" member either way. Calls go out with ids and wait for
// their answers; the server's notifications and its requests (approvals,
// questions) go to a Handler, in the order they came.
//
// The message types are written by hand from the schema Codex prints
// (`codex app-server generate-ts --experimental`), and cover what an ACP
// agent needs, not the whole protocol. The package imports only the
// standard library.
package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
)

// MaxLine is the longest line read: a thread's history can carry images
// as data URLs. A longer one is read to its end and skipped.
const MaxLine = 256 << 20

// Handler takes what the server sends of its own.
type Handler interface {
	// Notified takes a notification. Calls come one at a time, in the
	// order the server sent them; Notified may make calls of its own.
	Notified(method string, params json.RawMessage)
	// Asked answers a request (an approval, a question): its result, or
	// an error (an *Error as it is, another as Internal error). Each runs
	// on its own, started after every notification sent before it was
	// handled, and may wait as long as the user takes; ctx ends when the
	// connection does.
	Asked(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Replier is a Handler that is told once a request's answer is written:
// a fake server sends what follows an answer from there.
type Replier interface {
	Replied(method string, params json.RawMessage)
}

// Error is a JSON-RPC error, the server's answer to a call or ours to its
// request.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// JSON-RPC's error codes the protocol uses.
const (
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// ErrClosed is a call's error once the connection has ended.
var ErrClosed = errors.New("codex app-server connection closed")

// message is any line on the wire.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// answer is a response we write: result is always there, even null.
type answer struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result"`
}

type failure struct {
	ID    json.RawMessage `json:"id"`
	Error *Error          `json:"error"`
}

// Conn is a connection to an app-server.
type Conn struct {
	h   Handler
	log *slog.Logger

	w      io.Writer
	writes chan writeReq // to the writing goroutine, which alone writes w

	mu     sync.Mutex
	next   int64
	calls  map[string]chan message // by id
	err    error                   // why it ended; set once done closes
	done   chan struct{}
	ctx    context.Context //nolint:containedctx // the connection's life, for the server's requests
	cancel context.CancelFunc
}

// NewConn starts a connection reading r and writing w (the server's
// stdout and stdin); h takes what the server sends. log may be nil.
func NewConn(r io.Reader, w io.Writer, h Handler, log *slog.Logger) *Conn {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{h: h, log: log, w: w, writes: make(chan writeReq), calls: map[string]chan message{}, done: make(chan struct{}),
		ctx: ctx, cancel: cancel}
	q := &queue{more: make(chan struct{}, 1)}
	go c.writeLoop()
	go c.read(r, q)
	go q.run()
	return c
}

// Call sends method with params and decodes its result into result (nil:
// dropped). It returns the server's *Error, ctx's error, or ErrClosed.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	ch := make(chan message, 1)
	c.calls[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
	}()
	p, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := c.write(ctx, message{ID: json.RawMessage(id), Method: method, Params: p}); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s: %w", method, c.Err())
		}
		if m.Error != nil {
			return m.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(m.Result, result); err != nil {
			c.log.Warn("an app-server result doesn't decode", "method", method, "err", err)
			return fmt.Errorf("%s's result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(c.ctx, message{Method: method, Params: p})
}

// Done is closed once the connection has ended: the server's stdout
// ended or broke, or Close.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended (ErrClosed, wrapping a read's error if
// one broke it), or nil while it runs.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection: calls waiting fail with ErrClosed. It
// doesn't close the streams; whoever started the server stops it.
func (c *Conn) Close() error {
	c.end(nil)
	return nil
}

func (c *Conn) end(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = ErrClosed
	if cause != nil && !errors.Is(cause, io.EOF) {
		c.err = fmt.Errorf("%w: %w", ErrClosed, cause)
	}
	for id, ch := range c.calls {
		close(ch)
		delete(c.calls, id)
	}
	c.cancel()
	close(c.done)
}

// writeReq is a line for the writing goroutine, and where it says how
// the write went.
type writeReq struct {
	line []byte
	done chan error // buffered: the writer never waits on it
}

// write has v written as a line, after the lines before it, unless ctx
// ends or the connection does first. The writing goroutine alone writes,
// so a caller whose ctx ends while the server doesn't read returns at
// once; its line may still be written.
func (c *Conn) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	req := writeReq{line: append(b, '\n'), done: make(chan error, 1)}
	select {
	case c.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

// writeLoop writes the lines handed to it, one at a time, until the
// connection ends.
func (c *Conn) writeLoop() {
	for {
		select {
		case req := <-c.writes:
			_, err := c.w.Write(req.line)
			req.done <- err
		case <-c.done:
			return
		}
	}
}

// read reads lines until r ends, answering responses at once and queueing
// the rest for the handler.
func (c *Conn) read(r io.Reader, q *queue) {
	br := bufio.NewReaderSize(r, 64<<10)
	var err error
	for {
		var line []byte
		line, err = readLine(br)
		if errors.Is(err, errTooLong) {
			c.log.Warn("skipped an app-server line over the limit", "limit", MaxLine)
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 {
			c.take(line, q)
		}
		if err != nil {
			break
		}
	}
	c.end(err)
	q.close()
}

func (c *Conn) take(line []byte, q *queue) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		c.log.Warn("skipped an app-server line that isn't JSON", "bytes", len(line))
		return
	}
	switch {
	case m.Method == "" && m.ID != nil: // a response
		c.mu.Lock()
		ch := c.calls[string(m.ID)]
		delete(c.calls, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case m.ID != nil: // a request
		q.push(func() { go c.answer(m) })
	case m.Method != "":
		q.push(func() { c.h.Notified(m.Method, m.Params) })
	}
}

// answer runs the handler on a request and writes its answer.
func (c *Conn) answer(m message) {
	result, err := c.h.Asked(c.ctx, m.Method, m.Params)
	var werr error
	if err != nil {
		e, ok := errors.AsType[*Error](err)
		if !ok {
			e = &Error{Code: CodeInternal, Message: err.Error()}
		}
		werr = c.write(c.ctx, failure{ID: m.ID, Error: e})
	} else {
		werr = c.write(c.ctx, answer{ID: m.ID, Result: result})
	}
	if werr != nil {
		c.log.Debug("app-server request not answered", "method", m.Method, "err", werr)
		return
	}
	if r, ok := c.h.(Replier); ok {
		r.Replied(m.Method, m.Params)
	}
}

var errTooLong = errors.New("line too long")

// readLine reads a line without its newline, at most MaxLine bytes;
// a longer one is read to its end and errTooLong returned.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		part, err := br.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(part) > MaxLine+1 {
				tooLong, line = true, nil
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			if err == nil {
				err = errTooLong
			}
			return nil, err
		}
		return bytes.TrimSuffix(line, []byte("\n")), err
	}
}

// queue is an unbounded queue of the handler's work (its notifications
// and its requests' starts), so reading never waits on the handler
// (which may be making calls whose answers the reader takes).
type queue struct {
	mu     sync.Mutex
	items  []func()
	more   chan struct{}
	closed bool
}

func (q *queue) push(f func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, f)
	q.signal()
}

func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.signal()
}

func (q *queue) signal() {
	select {
	case q.more <- struct{}{}:
	default:
	}
}

// run runs the queue's items in order until it is closed and empty.
func (q *queue) run() {
	for {
		q.mu.Lock()
		items, closed := q.items, q.closed
		q.items = nil
		q.mu.Unlock()
		for _, f := range items {
			f()
		}
		if len(items) > 0 {
			continue
		}
		if closed {
			return
		}
		<-q.more
	}
}
