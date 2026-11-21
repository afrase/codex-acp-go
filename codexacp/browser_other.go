//go:build !darwin && !windows

package codexacp

import (
	"context"
	"os/exec"
)

// browserCommand opens page in the user's default browser, by xdg-open.
func browserCommand(ctx context.Context, page string) *exec.Cmd {
	return exec.CommandContext(context.WithoutCancel(ctx), "xdg-open", page) //nolint:gosec // page is a checked web URL, an argument, no shell
}
