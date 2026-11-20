# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## v0.2.0 (2026-11-21)

A minor version, as `appserver`'s item types changed (below).

- `appserver`: `Turn.Items`, `ItemNotification.Item` and `ItemEntry.Item` hold items as JSON, and `DecodeItem` decodes one, so an item with a field of an unexpected type fails alone, not the message or page that carries it; its error still names the item's kind and id. `Item`'s `Inputs`, `ReasoningContent` and `SearchAction` are fields decoded with the item, where they were methods. `InitializeResponse` types initialize's answer. A call's result that doesn't decode is logged.
- `codexacp`: an item that doesn't decode is logged (its kind, never its content) and skipped: the rest of the turn streams, and a load replays the rest of the history.
- `codexacp`: every notification or request from Codex that doesn't decode is logged at Warn with its method; a turn/completed that doesn't decode still ends the prompt by its turn's id.
- `codexacp`: a cancelled prompt ends cancelled 10 s after the cancel if Codex never sends turn/completed, where it waited for ever. A turn/start answer that doesn't decode interrupts the turn and fails the prompt.
- `codexacp`: a subagent's approvals and questions go to the session whose turn started it (and its subagents' too), as codex-acp sends them, where they were answered cancel unseen.
- `codexacp`: a command approval offers an exec or network rule only as Codex proposed it in the request, and a request whose options would share an id is refused. A request of a turn the user cancelled is answered cancel, even after the prompt has ended.
- `codexacp`: the log names Codex's user agent at start.
- README: what opening a folder as a trusted project means, and that every session starts in Auto review.

## v0.1.0 (2026-11-21)

The first release, tested against Codex CLI 0.163.0.

- `appserver`: a client for Codex's app-server (`codex app-server`): one JSON object a line, calls with ids, the server's notifications and requests in order, an unbounded queue so reading never waits on the handler, and typed messages written from the schema Codex prints. `Exec` starts the app-server as a plain child that stays in the caller's process group on Unix; its `Kill` closes the app-server's stdin and kills it if it is still running 2 s later.
- `appserver/appservertest`: a scripted fake app-server, for tests with no process or login.
- `codexacp`: an ACP agent on `appserver` with codex-acp's wire shape: sessions (new, load, resume, fork and fork at a message, list, delete, close), prompts with images and resources, cancel, steering, modes and config options (mode, collaboration mode, model, reasoning effort), streamed text and thoughts, tool calls (commands with `_meta.terminal_output_delta` and `_meta.terminal_exit`, edits as diffs, MCP, web search, image views, compaction, subagent calls), permissions, plan-mode questions as forms, MCP approvals and forms, plans, usage, titles, warnings, Codex's skills as `$name` commands, and MCP servers from the client.
- `cmd/codex-acp`: the agent as a program, for Zed and other ACP clients.
