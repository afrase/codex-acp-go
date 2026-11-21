//go:build unix

package codexacp

import (
	"testing"
)

// The browser's opener starts in a session of its own, outside the
// agent's process group (the command is checked, never started).
func TestBrowserOpenerDetached(t *testing.T) {
	const page = "https://auth.example.com/oauth/authorize?state=x"
	cmd := browserOpener(t.Context(), page)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("SysProcAttr %+v: the opener shares the agent's process group", cmd.SysProcAttr)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != page {
		t.Errorf("opener's arguments %q", cmd.Args)
	}
	if cmd.Process != nil {
		t.Error("the opener was started")
	}
}
