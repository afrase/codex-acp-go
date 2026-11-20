// Command codex-acp is an Agent Client Protocol agent for OpenAI's Codex
// CLI: an ACP client (Zed, or any other) starts it and talks ACP on its
// stdin and stdout, and it runs the codex the user installed as Codex's
// app-server (`codex app-server`). It needs no Node.
//
//	codex-acp            serve one client on stdin and stdout
//	codex-acp -version   print the version
//
// The codex it runs is $CODEX_PATH, else codex on PATH. It logs to
// stderr. Its own log lines never carry a prompt or a token, but when the
// app-server fails or exits the last line codex wrote to its stderr is
// logged as codex wrote it. SIGTERM and SIGINT stop the app-server, as
// the end of stdin does. It exits with 1 when the connection to the
// client broke.
package main

import (
	"fmt"
	"os"

	"github.com/afrase/codex-acp-go/codexacp"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version":
			fmt.Println("codex-acp", codexacp.Version) //nolint:forbidigo // -version's answer
			return
		default:
			fmt.Fprintln(os.Stderr, "usage: codex-acp [-version]\n\nAn ACP client starts codex-acp and talks to it on its stdin and stdout.")
			os.Exit(2)
		}
	}
	if codexacp.Main(codexacp.Config{}) != nil {
		os.Exit(1) // Main logged why
	}
}
