# codex-acp-go

[![Go Reference](https://pkg.go.dev/badge/github.com/afrase/codex-acp-go.svg)](https://pkg.go.dev/github.com/afrase/codex-acp-go)
[![CI](https://github.com/afrase/codex-acp-go/actions/workflows/ci.yml/badge.svg)](https://github.com/afrase/codex-acp-go/actions/workflows/ci.yml)

Run OpenAI's Codex CLI in Zed and other [Agent Client Protocol](https://agentclientprotocol.com)
(ACP) clients, without Node.

`codex-acp` is a single binary. It speaks ACP to the editor and runs your
own `codex` as Codex's app-server (`codex app-server`), the JSON-RPC
protocol Codex's own editor extensions use. It behaves like
[codex-acp](https://github.com/agentclientprotocol/codex-acp), the Node
agent that ACP clients use today, so a client that works with one works
with the other.

This module also holds the Go libraries that `codex-acp` is built on (see
[Use it as a library](#use-it-as-a-library)).

It is tested against Codex CLI 0.163.0.

> [!NOTE]
> This project is unofficial. It is not affiliated with or endorsed by
> OpenAI. It has no authentication code and never touches your
> credentials. It runs the unmodified `codex` that you installed, which
> signs in as usual.

## Why

- **No Node.** codex-acp needs Node, an npm install, and a Node process
  next to each Codex. `codex-acp` is one Go binary.
- **Your codex.** It runs the `codex` on your `PATH` (or `$CODEX_PATH`).
  Your login, `config.toml`, model providers, skills and MCP servers all
  apply, as in the terminal.
- **Shared sessions.** Sessions are Codex's own threads. Start one here and
  resume it in the terminal with `codex resume`, or the other way round.
- **Readable.** The protocol code is plain Go with no bundled SDK. You can
  read it and step through it.

## Install

You need:

- [Codex CLI](https://github.com/openai/codex), installed and signed
  in (run `codex` once in a terminal), or set up with a model provider in
  its `config.toml`.
- Go 1.27.4 or later, to build.

```sh
go install github.com/afrase/codex-acp-go/cmd/codex-acp@latest
```

The binary goes to `$(go env GOPATH)/bin`. Run `codex-acp -version` to
check it.

`codex-acp` runs whatever `codex` it is given. npm's `codex` is a small
Node launcher in front of Codex's native binary, so with an npm install
Node still runs that launcher. A Homebrew or standalone install of Codex
is native throughout.

On Linux, `go install` links the binary to the C library when a C
compiler is present. To copy it to another machine, such as an Alpine
container or an older distribution, build a static one instead:

```sh
CGO_ENABLED=0 go install github.com/afrase/codex-acp-go/cmd/codex-acp@latest
```

## Use it from Zed

1. Add the agent to Zed's `settings.json`. Use the binary's full path,
   because Zed does not start agents through your shell.

   ```json
   {
     "agent_servers": {
       "Codex (codex-acp-go)": {
         "type": "custom",
         "command": "/path/to/go/bin/codex-acp",
         "args": [],
         "env": {}
       }
     }
   }
   ```

2. In the Agent Panel, open the new-thread menu and pick the agent.

If `codex` is not on the `PATH` that Zed gives agents, set it in `env`:
`"env": {"CODEX_PATH": "/opt/homebrew/bin/codex"}`.

**Other ACP clients** start it the same way: run `codex-acp` and speak ACP
on its stdin and stdout. It logs to stderr. Its own log lines never hold a
prompt or a token, but when Codex fails or exits, the last line `codex`
wrote to its stderr is logged as it is.

## Use it as a library

The module has three packages:

| Package | What it is |
|---|---|
| `codexacp` | The ACP agent, built on `appserver` and [acp-go](https://github.com/afrase/acp-go). `Serve` it on any streams, or call `Main` for stdin and stdout. |
| `appserver` | A client for Codex's app-server protocol: framing, calls, the server's notifications and requests, and typed messages for what the agent uses. Standard library only. |
| `appserver/appservertest` | A scripted fake app-server. Test code built on `appserver` or `codexacp` with no process and no login. |

The agent in a program of your own, with the codex to run and a log of
your choice:

```go
package main

import (
	"log/slog"
	"os"

	"github.com/afrase/codex-acp-go/codexacp"
)

func main() {
	cfg := codexacp.Config{
		CodexPath: "/opt/homebrew/bin/codex",
		Log:       slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	if err := codexacp.Main(cfg); err != nil {
		os.Exit(1)
	}
}
```

- `codexacp.Serve` runs the agent on any reader and writer, until the
  client goes or its context ends.
- `Config.Spawn` starts the app-server another way (in a container, say).
  The default, `appserver.Exec`, runs it as a plain child that stays in
  the agent's process group on Unix, so a client that kills the agent's
  group kills Codex too.
- Each package's documentation has runnable examples:
  `go doc github.com/afrase/codex-acp-go/codexacp`.

## What's supported

| Area | Support |
|---|---|
| Sessions | New, load (replays the history), resume, fork (whole, or up to a message, with the JetBrains fork-at extension), list (by folder), delete (Codex archives the thread), close. |
| Prompts | Text, images, resource links and embedded resources. Cancel (Codex interrupts the turn). A second prompt waits for the running turn. |
| Steering | A message sent during a turn joins it (`_session/steering`). With no turn running, it answers `promptRequired` when the client asks for that, or starts a turn. |
| Modes | Read-only, Workspace access, Auto review (the default) and Full access, as codex-acp has them. |
| Config options | Mode, collaboration mode (Default, Plan), model, and reasoning effort for a model that has levels. |
| Turn output | Text and thoughts, streamed. Codex's to-do plan as a plan. Usage and the context window. The session's title. Codex's warnings, as text. |
| Tool calls | Commands, with their output streamed (`_meta.terminal_output_delta`, `_meta.terminal_exit`). File edits, as diffs. MCP tool calls. Web search. Image views. Compaction. Subagent calls, as plain calls. |
| Permissions | Every approval Codex asks for goes to the client: commands (with Codex's own choices, such as "don't ask again" for a command prefix), file edits, and MCP tool approvals. The agent never allows anything itself. |
| Questions | A plan-mode question (`request_user_input`) as a form, each answer with an optional note, if the client takes form elicitation. An MCP server's form too. |
| Commands | Codex's skills, offered as `$name` commands. |
| MCP | MCP servers from the client (stdio, HTTP), added to the thread's config. |

Not yet:

- **codex-acp's own slash commands**: `/plan`, `/compact`, `/review`,
  `/review-branch`, `/review-commit`, `/status`, `/mcp`, `/skills`,
  `/goal`, `/rename`, `/logout`.
- **The fast-mode option** (Codex's service tier).
- **Subagents as sessions of their own.** A subagent shows as a plain tool
  call.
- **Background terminals.** A command Codex leaves running shows as
  running until it ends; nothing stops it from the client.
- **Rate limits**, and the AI-generated title Codex can make.
- **Skipping a client's MCP server** that your Codex config already names.
- **A question's auto-resolution timeout**, and requests for extra sandbox
  permissions (refused).
- **Recovery after the app-server exits.** The agent quits, and the client
  sees it go.
- **ACP's `authenticate`.** Sign in with `codex` itself. Without a login or
  provider that Codex needs, `session/new` answers Authentication
  required.
- **MCP servers over SSE**, and ACP's own MCP transport.
- **Windows** is not tested. The module builds there (CI checks that), but
  the tests run on macOS and Linux only.

## How it relates to codex-acp

**codex-acp** (JetBrains, Apache-2.0) is the Node ACP agent for Codex.
`codexacp` follows its behaviour where a client sees it, so clients need
no changes: Codex's thread ids as session ids and its item ids as message
ids, the modes, config options and permission options, the
`_meta.terminal_*` updates, the plan-mode question form, and errors from
Codex as `Internal error` with Codex's words in `data.details`. The code
is written new in Go.

It departs in two places:

- With no turn running, a steer answers `promptRequired` to a client that
  asks for it; codex-acp starts a turn for every client.
- Codex's to-do plan goes as a `plan` update to every client; codex-acp
  sends it only to clients that declare plans.

As codex-acp does, each session opens its folder as a trusted project, so
the folder's `.codex/config.toml` applies.

The app-server protocol is Codex's own, and the agent asks for its
experimental API, as codex-acp does. To limit breakage, `appserver`:

- parses leniently: it reads only the fields it uses, and decodes each
  thread item on its own, so an item that doesn't decode is logged (its
  kind, never its content) and skipped, and the rest of its turn or
  history page still comes through;
- types only what the agent sends and reads, from the schema Codex prints
  (`codex app-server generate-ts --experimental`);
- pins no `codex` version.

A change in Codex can still break it, as it can break codex-acp.

[claude-acp-go](https://github.com/afrase/claude-acp-go) is the same idea
for Claude Code.

## Development

```sh
make check   # build, vet, cross-builds, lint, govulncheck, the tests with -race
```

golangci-lint and govulncheck are pinned in `tools/lint` and run through
`go tool`, so you install nothing.

The tests run a fake app-server (`appserver/appservertest`) and need no
`codex` or login.

### Live tests

`codexacp`'s live tests drive the agent on a real Codex: prompts,
commands and their approvals, an edit, cancel, steering, reload, fork,
list and delete, and plan-mode questions. They are skipped unless you ask
for them, and CI never runs them. They talk to a model, so they are slow
and cost tokens.

```sh
CODEX_ACP_LIVE=1 CODEX_ACP_LIVE_HOME=~/.codex \
  go test -run TestLive -v ./codexacp/
```

- `CODEX_ACP_LIVE_HOME` is a `CODEX_HOME` that can answer a prompt. Each
  test copies its `config.toml`, `auth.json` and `installation_id` to a
  temp folder and runs Codex there, so your sessions and settings are
  never written.
- The `codex` run is `$CODEX_PATH`, else `codex` on `PATH`.
- `CODEX_ACP_LIVE_LOGS=<folder>` keeps each test's traffic there: the
  app-server's lines and the ACP updates (never the environment).

## Licence and credits

MIT: see [LICENSE](LICENSE). The module follows the behaviour of
[codex-acp](https://github.com/agentclientprotocol/codex-acp), Apache-2.0,
Copyright JetBrains s.r.o., and its app-server messages are written from
the schema that [Codex](https://github.com/openai/codex) prints,
Apache-2.0, Copyright OpenAI ([NOTICE](NOTICE)).

Codex and OpenAI are trademarks of OpenAI. When you use this software,
you use Codex, under the terms of your OpenAI account or your model
provider's.
