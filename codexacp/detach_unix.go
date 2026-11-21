//go:build unix

package codexacp

import (
	"os/exec"
	"syscall"
)

// detach has cmd start in a session, and so a process group, of its own:
// a signal to the agent's group doesn't reach it.
func detach(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
