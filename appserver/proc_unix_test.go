//go:build unix

package appserver

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exec keeps the app-server in this process's group, so a client that
// kills the agent's group kills the app-server too; Kill ends it (here by
// closing its stdin), and its stdout then ends.
func TestExecSharesTheGroup(t *testing.T) {
	p, err := Exec(Spec{Path: "/bin/sh", Args: []string{"-c", "echo $$; echo gone >&2; exec cat"}})
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(p.Out)
	line, err := out.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != syscall.Getpgrp() {
		t.Errorf("the app-server's group is %d, ours %d", pgid, syscall.Getpgrp())
	}
	p.Kill()
	p.Kill() // a second Kill does nothing
	if _, err := io.ReadAll(out); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("stdout after Kill: %v", err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Error("the app-server outlived Kill")
	}
	if got := p.Said(); got != "gone" {
		t.Errorf("Said %q", got)
	}
}

// The app-server exits once its stdin closes, as codex app-server does;
// Exec's stdout then ends with it.
func TestExecStdoutEndsWithTheProgram(t *testing.T) {
	p, err := Exec(Spec{Path: "/bin/sh", Args: []string{"-c", "cat; echo bye"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := io.WriteString(p.In, "hi\n"); err != nil {
		t.Fatal(err)
	}
	_ = p.In.Close()
	b, err := io.ReadAll(p.Out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hi\nbye\n" {
		t.Errorf("stdout %q", b)
	}
}

// An app-server that doesn't exit when its stdin closes is killed once
// the grace is up.
func TestExecKillsAfterTheGrace(t *testing.T) {
	p, err := Exec(Spec{Path: "/bin/sh", Args: []string{"-c", "trap '' TERM; echo $$; exec sleep 100"}})
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p.Out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Kill()
	if took := time.Since(start); took < stopGrace || took > stopGrace+5*time.Second {
		t.Errorf("Kill took %s", took)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Error("the app-server outlived Kill")
	}
}
