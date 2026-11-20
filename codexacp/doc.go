// Package codexacp is an Agent Client Protocol (ACP) agent for OpenAI's
// Codex CLI: it speaks ACP to a client (an editor such as Zed, or any ACP
// client) on one side, and runs Codex's app-server (`codex app-server`,
// through the appserver package) on the other. It needs no Node: it is a
// Go replacement for codex-acp, the Node agent ACP clients run today.
//
// It is unofficial and not affiliated with OpenAI, and it contains no
// authentication code: Codex signs in as it always does.
//
// # Compatibility
//
// Its behaviour follows codex-acp's where a client sees it, so a client
// written for that one works with this one: Codex's thread ids as session
// ids and its item ids as message ids, the same modes and config options
// (mode, collaboration_mode, model, reasoning_effort), the same permission
// options, a command's output as _meta.terminal_output_delta and its end
// as _meta.terminal_exit, a plan-mode question as a form with a note per
// question, and the JetBrains fork-at extension. Sessions are Codex's own:
// one started here resumes in Codex's terminal (`codex resume`) and the
// other way round, and one started by codex-acp loads here.
//
// # Running it
//
// Main runs the agent on stdin and stdout, as an ACP client starts an
// agent; the module's cmd/codex-acp is that program. Serve runs it on any
// streams. One app-server serves every session of a connection; each
// session is a thread. A prompt is one Codex turn, a cancel interrupts
// it, and a message steered into a running turn joins it. Every approval
// Codex asks for goes to the client; the agent never allows anything
// itself.
//
// # Trust
//
// The client is trusted as the user. As codex-acp does, each session's
// folder is opened as a trusted project, so its .codex/config.toml
// applies, and the client's MCP servers are added to Codex's config for
// the thread: a stdio server is a command Codex runs.
//
// The code is written fresh in Go; the module's NOTICE names what it
// follows.
package codexacp

// Name is the agent's name in its initialize answer; clients may key on
// it.
const Name = "codexacp"

// Version is the agent's version in its initialize answer: the version
// the Go build recorded for this module, or "devel".
var Version = moduleVersion()
