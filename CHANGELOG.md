# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## v0.1.0 (2026-11-21)

The first release, tested against Codex CLI 0.163.0.

- `appserver`: a client for Codex's app-server (`codex app-server`): one JSON object a line, calls with ids, the server's notifications and requests in order, an unbounded queue so reading never waits on the handler, and typed messages written from the schema Codex prints. `Exec` starts the app-server as a plain child that stays in the caller's process group on Unix; its `Kill` closes the app-server's stdin and kills it if it is still running 2 s later.
- `appserver/appservertest`: a scripted fake app-server, for tests with no process or login.
- `codexacp`: an ACP agent on `appserver` with codex-acp's wire shape: sessions (new, load, resume, fork and fork at a message, list, delete, close), prompts with images and resources, cancel, steering, modes and config options (mode, collaboration mode, model, reasoning effort), streamed text and thoughts, tool calls (commands with `_meta.terminal_output_delta` and `_meta.terminal_exit`, edits as diffs, MCP, web search, image views, compaction, subagent calls), permissions, plan-mode questions as forms, MCP approvals and forms, plans, usage, titles, warnings, Codex's skills as `$name` commands, and MCP servers from the client.
- `cmd/codex-acp`: the agent as a program, for Zed and other ACP clients.
