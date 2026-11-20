package codexacp

import (
	"context"
	"os/exec"
)

// openBrowser opens page in the user's default browser.
func openBrowser(ctx context.Context, page string) error {
	// The opener outlives the request.
	return launch(exec.CommandContext(context.WithoutCancel(ctx), "open", page)) //nolint:gosec // page is a checked web URL, an argument, no shell
}
