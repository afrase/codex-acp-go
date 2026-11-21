//go:build windows

package codexacp

import (
	"errors"
	"os/exec"
	"syscall"
)

// createBreakawayFromJob is CREATE_BREAKAWAY_FROM_JOB.
const createBreakawayFromJob = 0x01000000

// detach has cmd leave the job object the agent runs in, if any: a client
// that ends the agent's job (as acp-go's Start does) doesn't end the
// browser the opener may have become.
func detach(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createBreakawayFromJob}
	return cmd
}

// detachRefused reports whether a detached start failed because the
// agent's job doesn't let a process leave it.
func detachRefused(err error) bool { return errors.Is(err, syscall.ERROR_ACCESS_DENIED) }
