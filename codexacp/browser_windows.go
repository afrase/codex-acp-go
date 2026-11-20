package codexacp

import (
	"context"
	"os/exec"
)

// openBrowser opens page in the user's default browser, by the URL
// handler.
func openBrowser(ctx context.Context, page string) error {
	// The opener outlives the request.
	return launch(exec.CommandContext(context.WithoutCancel(ctx), "rundll32", "url.dll,FileProtocolHandler", page)) //nolint:gosec // page is a checked web URL, an argument, no shell
}
