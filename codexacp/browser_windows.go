package codexacp

import (
	"context"
	"os/exec"
)

// browserCommand opens page in the user's default browser, by the URL
// handler.
func browserCommand(ctx context.Context, page string) *exec.Cmd {
	return exec.CommandContext(context.WithoutCancel(ctx), "rundll32", "url.dll,FileProtocolHandler", page) //nolint:gosec // page is a checked web URL, an argument, no shell
}
