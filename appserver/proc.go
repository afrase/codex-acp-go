package appserver

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Spec is how to start an app-server: codex's path, its arguments
// (`app-server`), folder and environment (nil: this process's).
type Spec struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Proc is a started app-server: its stdin and stdout, a way to stop it
// (Kill, which waits for its exit), and the last line it wrote to stderr,
// which says why it stopped when it did. A nil Kill or Said does nothing.
type Proc struct {
	In   io.WriteCloser
	Out  io.Reader
	Kill func()
	Said func() string
}

// Spawner starts an app-server. Exec is the real one; a test's is a fake
// (appservertest).
type Spawner func(Spec) (Proc, error)

// stopGrace is how long Kill waits for the app-server to exit once its
// stdin is closed, before it kills it.
const stopGrace = 2 * time.Second

// waitDelay is how long Exec's app-server's stderr is read after it exits,
// and how long its stdout is left to its reader after that: a process it
// started that is still running may hold them open.
const waitDelay = time.Second

// Exec starts the app-server as a plain child of this process.
//
// On Unix it stays in this process's process group, as codex-acp's child
// app-server does: a client that stops its agent by killing the agent's
// group (as ACP clients do) takes the app-server, and what runs in its
// group, with it, even when the agent had no time to stop it. The
// app-server also exits by itself once its stdin closes, so it doesn't
// outlive an agent killed alone either.
//
// Kill closes the app-server's stdin, so it ends as it does when its
// client goes, and kills it (it alone) if it is still running 2 s later.
// It returns once the app-server has exited.
//
// On Windows, which has no process groups of this kind, it is a plain
// child too; when Kill has to kill it, codex alone is terminated, and a
// command it started may run on.
//
// Its stderr is dropped, but for its last line (Said).
func Exec(s Spec) (Proc, error) {
	cmd := exec.Command(s.Path, s.Args...) //nolint:gosec,noctx // the user's codex; it lives until Kill, not a context
	cmd.Dir, cmd.Env = s.Dir, s.Env
	cmd.WaitDelay = waitDelay
	said := &lastLine{}
	cmd.Stderr = said
	in, err := cmd.StdinPipe()
	if err != nil {
		return Proc{}, err
	}
	// Our own pipe, not cmd's: Wait closes that one as the program exits,
	// under a reader that may not yet have read what it wrote last.
	r, w, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		return Proc{}, err
	}
	cmd.Stdout = w
	err = cmd.Start()
	_ = w.Close() // the app-server has its own copy
	if err != nil {
		_ = r.Close()
		return Proc{}, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = in.Close() // the app-server exits once its stdin ends
			select {
			case <-exited:
			case <-time.After(stopGrace):
				_ = cmd.Process.Kill()
				<-exited
			}
			_ = r.Close()
		})
	}
	return Proc{In: in, Out: r, Said: said.String, Kill: kill}, nil
}

// lastLine keeps the last line with anything in it written to it.
type lastLine struct {
	mu   sync.Mutex
	line []byte
	last string
}

func (l *lastLine) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range b {
		switch {
		case c == '\n':
			if s := strings.TrimSpace(string(l.line)); s != "" {
				l.last = s
			}
			l.line = l.line[:0]
		case len(l.line) < 500:
			l.line = append(l.line, c)
		}
	}
	return len(b), nil
}

func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}
