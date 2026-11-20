//go:build unix

package codexacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	acp "github.com/afrase/acp-go/unstable"
)

// The test binary plays two parts for TestAppServerEndsWithTheAgent, as
// CODEXACP_TEST_ROLE says: the agent (Main) and a stand-in codex.
func TestMain(m *testing.M) {
	switch os.Getenv("CODEXACP_TEST_ROLE") {
	case "agent":
		if Main(Config{}) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "codex":
		os.Exit(standInCodex())
	}
	m.Run()
}

// standInCodex answers initialize and then waits for its stdin to end, as
// codex app-server does; with CODEXACP_TEST_STUBBORN set it ignores that
// and SIGTERM, so only SIGKILL ends it. It writes its pid to the file
// $CODEXACP_TEST_PID names.
func standInCodex() int {
	stubborn := os.Getenv("CODEXACP_TEST_STUBBORN") != ""
	if stubborn {
		signal.Ignore(syscall.SIGTERM)
	}
	if err := os.WriteFile(os.Getenv("CODEXACP_TEST_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 2
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) == nil && req.Method == "initialize" {
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"userAgent":"stand-in"}}`+"\n", req.ID)
		}
	}
	for stubborn {
		time.Sleep(time.Hour)
	}
	return 0
}

// quietClient is a client that asks nothing of the agent's requests.
type quietClient struct{ acp.UnimplementedClient }

// However the agent ends (its group killed, as a client stops an agent
// that overstays its grace; itself killed alone; SIGTERM; its stdin's
// end), its app-server doesn't outlive it.
func TestAppServerEndsWithTheAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("runs processes")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		stubborn bool // the app-server ignores its stdin's end and SIGTERM
		end      func(*acp.AgentConn, *exec.Cmd)
		clean    bool // the agent exits by itself, with 0
	}{
		{"group killed", true, func(c *acp.AgentConn, _ *exec.Cmd) { _ = c.Close() }, false},
		{"agent killed alone", false, func(_ *acp.AgentConn, cmd *exec.Cmd) { _ = cmd.Process.Kill() }, false},
		{"SIGTERM", true, func(_ *acp.AgentConn, cmd *exec.Cmd) { _ = cmd.Process.Signal(syscall.SIGTERM) }, true},
		{"stdin's end", true, func(c *acp.AgentConn, _ *exec.Cmd) { _ = c.Shutdown(context.Background()) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			codex := filepath.Join(dir, "codex")
			script := fmt.Sprintf("#!/bin/sh\nCODEXACP_TEST_ROLE=codex exec %q \"$@\"\n", self)
			if err := os.WriteFile(codex, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(self)
			cmd.Env = append(os.Environ(), "CODEXACP_TEST_ROLE=agent", "CODEX_PATH="+codex,
				"CODEXACP_TEST_PID="+pidFile, "GORACE=atexit_sleep_ms=0")
			if tc.stubborn {
				cmd.Env = append(cmd.Env, "CODEXACP_TEST_STUBBORN=1")
			}
			var log strings.Builder
			cmd.Stderr = &log
			conn, err := acp.Start(cmd, func(*acp.AgentConn) acp.Client { return quietClient{} }) // in a group of its own, as a client runs it
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = conn.Close()
				<-conn.Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := conn.Initialize(ctx, &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) }) // if the test failed

			tc.end(conn, cmd)
			<-conn.Done()
			eventually(t, "the stand-in codex's end", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
			if tc.clean && !cmd.ProcessState.Success() {
				t.Errorf("the agent ended %v\n%s", cmd.ProcessState, log.String())
			}
		})
	}
}

// An agent whose connection to the client breaks (here its stdin can't
// be read) exits with an error, not 0 as if the client had gone.
func TestBrokenConnectionFails(t *testing.T) {
	if testing.Short() {
		t.Skip("runs processes")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdin, err := os.Open(dir) // read(2) of a folder fails: EISDIR
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "CODEXACP_TEST_ROLE=agent", "GORACE=atexit_sleep_ms=0")
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("the agent ended %v, want exit status 1\n%s", err, out)
	}
}

// eventually waits up to 10 s for ok, failing the test with what.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
