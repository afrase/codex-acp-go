# Changelog

All notable changes to this module. It follows [Semantic Versioning](https://semver.org); until 1.0, a minor version may change the API.

## v0.3.3 (unreleased)

No API change.

- `codexacp`: plan-mode questions are built with acp-go's typed elicitation (`CreateElicitation`, `ElicitationProperties`), which keeps the fields' order as before. An answer to an MCP server's form, or to the questions, whose values aren't strings, numbers, booleans or string arrays (as ACP's `ElicitationContentValue` allows) is now taken as cancel, where it was passed on to Codex.

## v0.3.2 (2026-11-21)

No API change. Needs acp-go v0.5.0.

- `codexacp`: a new session's (or fork's) available commands wait for its answer through acp-go's `AfterResponse`, not by matching the bytes the connection writes, so a change to acp-go's encoding can't break it.
- `codexacp`, on Windows: the browser the sign-in opens leaves the agent's job object (`CREATE_BREAKAWAY_FROM_JOB`), so a client that ends the agent's job once signed in (as acp-go's `Start` does from v0.5.0) doesn't close it. Where the job doesn't allow that, the opener starts in the job, as before.
- `codexacp`: `Main` waits for SIGTERM and SIGINT with `signal.NotifyContext`. The log line on a signal now reads `signal="terminated signal received"` where it read `signal=terminated`.
- Tests: one wait loop, one serve path for the live tests, and shared prompt and question helpers.

## v0.3.1 (2026-11-21)

- Builds on acp-go v0.4.1: a call whose context has ended is never sent, a response with neither result nor error fails its call, a request reusing a live id is refused, and requests wait while 64 answers are unread.
- `codexacp`: `session/cancel` no longer waits for Codex to answer `turn/interrupt`. It ran on the goroutine that reads the client, so a slow Codex held up every later message, and a client gone mid-turn could leave Serve waiting for ever. The interrupt is sent on its own, within the 10 s grace.
- `appserver`: a call gives up on its write when its ctx ends, where an app-server that stopped reading its stdin held every call and answer.
- `codexacp`: a `session/cancel` also stops a prompt sent before it that is still setting up or waiting for the running turn (acp-go's `SequenceFromContext`): it ends cancelled and starts no turn.
- `codexacp`: a prompt waiting for the running turn ends cancelled when its session closes or is loaded again, and starts no turn on it; it ran a turn no one heard and never returned.
- `codexacp`: a prompt withdrawn (`$/cancel_request`) while `turn/start` is unanswered still has the turn Codex starts interrupted.
- `codexacp`: an edit that removes a line starting with `-- ` (a SQL or Lua comment) keeps its diff: a hunk runs as many lines as its header counts.
- `codexacp`: the sign-in page's opener runs in a session of its own on Unix, so a client that ends the agent's process group doesn't close the browser.
- `codexacp`: Serve returns an error when the app-server exits on its own, and `codex-acp` exits 1, where both said nothing went wrong.
- `codexacp`: a config option set while Codex changes the collaboration mode is no longer undone by that change.
- `codexacp`: a `Config.Spawn` whose `Proc` leaves `Kill` or `Said` nil no longer panics.
- `codexacp`: a new session, load or resume that fails once its thread is open unsubscribes from the thread.
- `codexacp`: an approval or question of a turn cancelled before Codex said its id is answered cancel without reaching the user.
- `codexacp`: `session/close` of a session that isn't open succeeds, as codex-acp's does. `session/delete` of an open session closes it first, and loading an open session again interrupts its turn.
- `appserver`: `DecodeItem`'s error names the item's id as well as its kind, as the v0.2.0 notes said.
- `go.mod` asks for Go 1.26, what the code needs, where it asked for 1.27.4.

## v0.3.0 (2026-11-21)

- `codexacp`: sign-in through ACP, as codex-acp offers it. `initialize` lists the `api-key` and `chat-gpt` auth methods (ChatGPT not with `NO_BROWSER` set), and `authenticate` has Codex sign in: with an API key from the request or `$CODEX_API_KEY` / `$OPENAI_API_KEY`, or with ChatGPT, whose page opens in the browser and whose login is cancelled when the client cancels the request. A failed login answers Authentication required ("Sign-in did not complete"). `Config.OpenURL` opens the page another way.
- `appserver`: `LoginStartParams`, `LoginStartResponse`, `LoginCancelParams` and `LoginCompleted` type the account login messages; `AccountReadResponse.AccountType` is the login's type.

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
