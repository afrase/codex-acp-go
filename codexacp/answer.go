package codexacp

import (
	"bytes"
	"io"
	"sync"
)

// afterAnswer is the agent's writer to the client, which watches for the
// answers that open sessions: a client takes a session's updates once it
// knows its id, so what a new session sends first (its commands) waits
// for its answer to be written.
type afterAnswer struct {
	w    io.Writer
	mu   sync.Mutex
	then map[string]func() // by session id
}

// after has fn run once the answer naming session sid is written.
func (aa *afterAnswer) after(sid string, fn func()) {
	aa.mu.Lock()
	defer aa.mu.Unlock()
	if aa.then == nil {
		aa.then = map[string]func(){}
	}
	aa.then[sid] = fn
}

func (aa *afterAnswer) Write(p []byte) (int, error) {
	n, err := aa.w.Write(p)
	if err == nil {
		if fn := aa.answered(p); fn != nil {
			go fn() // on its own: the writer that runs this writes what it sends
		}
	}
	return n, err
}

// An answer is written as answerHead, its id, then sessionResult and the
// session's id when it opens one (session/new's, session/fork's).
var (
	answerHead    = []byte(`{"jsonrpc":"2.0","id":`)
	resultKey     = []byte(`,"result":`)
	sessionResult = []byte(`,"result":{"sessionId":"`)
)

// answered is what waits for p, if p is an answer opening a session.
func (aa *afterAnswer) answered(p []byte) func() {
	aa.mu.Lock()
	defer aa.mu.Unlock()
	if len(aa.then) == 0 {
		return nil
	}
	rest, ok := bytes.CutPrefix(p, answerHead)
	i := bytes.Index(rest, resultKey)
	if !ok || i < 0 || bytes.Contains(rest[:i], []byte(`"method"`)) || !bytes.HasPrefix(rest[i:], sessionResult) {
		return nil
	}
	sid, _, _ := bytes.Cut(rest[i+len(sessionResult):], []byte(`"`))
	fn := aa.then[string(sid)]
	delete(aa.then, string(sid))
	return fn
}
