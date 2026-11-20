package codexacp

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// tools is what a session remembers of its tool calls while they run:
// which commands show a terminal and which streamed output, which
// reasonings came in pieces, which files a change touches. It follows
// codex-acp's reporters (CommandReporter, FileChangeReporter and the
// rest) as they render for a client that isn't JetBrains AIR.
type tools struct {
	mu       sync.Mutex
	terminal map[string]bool // started commands: true for one shown as a terminal
	streamed map[string]bool // commands that sent output
	thoughts map[string]bool // reasonings that came in pieces
	viewed   map[string]bool // image views already shown
	changes  map[string]appserver.Item
	sources  map[string]string   // started commands' sources
	mcp      map[string][]string // running MCP calls by server, which an approval names
	initDone bool
}

func (t *tools) init() {
	if !t.initDone {
		t.terminal, t.streamed, t.thoughts, t.viewed = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		t.changes, t.sources, t.mcp = map[string]appserver.Item{}, map[string]string{}, map[string][]string{}
		t.initDone = true
	}
}

// thought notes a reasoning that came in pieces: its whole text at its
// end isn't sent again.
func (t *tools) thought(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	t.thoughts[id] = true
}

// turnEnded forgets what only a turn's permission requests need.
func (t *tools) turnEnded() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	clear(t.changes)
	clear(t.sources)
	clear(t.mcp)
}

// pendingMCP takes the call an MCP tool approval of server is about: the
// one call of that server running, or "" when there are none or several,
// as codex-acp correlates them (the request names no call).
func (t *tools) pendingMCP(server string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	ids := t.mcp[server]
	if len(ids) != 1 {
		return ""
	}
	delete(t.mcp, server)
	return ids[0]
}

// item is the updates an item's start or end makes.
func (t *tools) item(started bool, it appserver.Item) []acp.SessionUpdate {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	switch it.Type {
	case "commandExecution":
		if started {
			return []acp.SessionUpdate{t.commandStarted(it)}
		}
		return []acp.SessionUpdate{t.commandCompleted(it)}
	case "fileChange":
		if started {
			t.changes[it.ID] = it
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: "Editing files", Kind: "edit",
				Status: status(it.Status), Content: diffs(it.Changes)})}
		}
		delete(t.changes, it.ID)
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Status: endStatus(it.Status)})}
	case "reasoning":
		if started {
			return nil
		}
		if t.thoughts[it.ID] {
			delete(t.thoughts, it.ID)
			return nil
		}
		parts := it.Summary
		if len(parts) == 0 {
			parts = it.ReasoningText()
		}
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return []acp.SessionUpdate{chunk("agent_thought_chunk", it.ID, strings.Join(kept, "\n\n"))}
	}
	return t.other(started, it)
}

// other is the updates of a tool that isn't a command or an edit: an MCP
// call, a dynamic tool, a web search, an image view, a compaction, a
// subagent's call.
func (t *tools) other(started bool, it appserver.Item) []acp.SessionUpdate {
	switch it.Type {
	case "mcpToolCall":
		if started {
			t.mcp[it.Server] = append(t.mcp[it.Server], it.ID)
		} else {
			t.mcp[it.Server] = slices.DeleteFunc(t.mcp[it.Server], func(id string) bool { return id == it.ID })
		}
		input := raw(map[string]any{"server": it.Server, "tool": it.Tool, "arguments": it.Arguments})
		meta := map[string]any{"is_mcp_tool_call": true}
		if started {
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: "mcp." + it.Server + "." + it.Tool, Kind: "execute",
				Status: status(it.Status), RawInput: input, RawOutput: mcpOutput(it), Meta: meta})}
		}
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Status: endStatus(it.Status),
			RawInput: input, RawOutput: mcpOutput(it), Meta: meta})}
	case "dynamicToolCall":
		name := it.Tool
		if it.Namespace != nil {
			name = *it.Namespace + it.Tool
		}
		input := raw(map[string]any{"arguments": it.Arguments})
		if started {
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Name: name, Title: it.Tool, Kind: "execute",
				Status: status(it.Status), RawInput: input})}
		}
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Name: name, Status: endStatus(it.Status)})}
	case "webSearch":
		input := raw(map[string]any{"type": it.Type, "id": it.ID, "query": it.Query, "action": orNull(it.Action)})
		if started {
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: searchTitle(it), Kind: "search",
				Status: "in_progress", RawInput: input})}
		}
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Title: searchTitle(it), Status: "completed", RawInput: input})}
	case "imageView":
		if !started && t.viewed[it.ID] {
			delete(t.viewed, it.ID)
			return nil
		}
		if started {
			t.viewed[it.ID] = true
		}
		link := acp.ContentBlock{Type: "resource_link", ResourceLink: &acp.ResourceLink{Name: it.Path, URI: it.Path}}
		return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Name: "view_image", Title: "View Image " + it.Path,
			Kind: "read", Status: "completed", Content: []acp.ToolCallContent{content(link)},
			Locations: []acp.ToolCallLocation{{Path: it.Path}}, RawInput: raw(map[string]any{"path": it.Path})})}
	case "contextCompaction":
		if started {
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: "Compact conversation", Kind: "think", Status: "in_progress"})}
		}
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Title: "Compact conversation", Status: "completed"})}
	case "collabAgentToolCall":
		input := raw(map[string]any{"prompt": it.Prompt, "senderThreadId": it.SenderThreadID, "receiverThreadIds": it.ReceiverThreadIDs,
			"agentsStates": it.AgentsStates, "model": it.Model, "reasoningEffort": it.ReasoningEffort, "status": it.Status})
		if started {
			return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: it.Tool, Kind: "other", Status: status(it.Status), RawInput: input})}
		}
		return []acp.SessionUpdate{callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Title: it.Tool, Status: status(it.Status), RawInput: input})}
	}
	return nil
}

// commandStarted is a command's tool call: a terminal for a command Codex
// can't read as one read, search or listing; the action otherwise.
func (t *tools) commandStarted(it appserver.Item) acp.SessionUpdate {
	a := action(it)
	terminal := a.Type == "unknown"
	t.terminal[it.ID], t.sources[it.ID] = terminal, it.Source
	delete(t.streamed, it.ID)
	c := acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Name: commandName(it.Source), Status: status(it.Status)}
	switch a.Type {
	case "read":
		c.Kind, c.Title = "read", "Read file '"+deref(a.Path)+"'"
		c.Locations = []acp.ToolCallLocation{{Path: deref(a.Path)}}
	case "search":
		c.Kind, c.Title = "search", searchFor(deref(a.Query), deref(a.Path))
	case "listFiles":
		c.Kind, c.Title = "read", "List files"
		if p := deref(a.Path); p != "" {
			c.Title = "List files in '" + p + "'"
		}
	default:
		c.Kind, c.Title = "execute", stripShell(a.Command)
		c.RawInput = raw(map[string]any{"command": a.Command, "cwd": it.Cwd})
		c.Content = []acp.ToolCallContent{{Type: "terminal", Terminal: &acp.Terminal{TerminalID: acp.TerminalID(it.ID)}}}
		c.Meta = map[string]any{"terminal_info": map[string]any{"cwd": it.Cwd, "terminal_id": it.ID}}
	}
	return call(c)
}

// commandCompleted ends a command's call: output that didn't stream goes
// once, as a piece; a terminal's exit code in terminal_exit, another
// command's in rawOutput.
func (t *tools) commandCompleted(it appserver.Item) acp.SessionUpdate {
	terminal, known := t.terminal[it.ID]
	if !known {
		terminal = action(it).Type == "unknown"
	}
	streamed := t.streamed[it.ID]
	delete(t.terminal, it.ID)
	delete(t.streamed, it.ID)
	delete(t.sources, it.ID)
	u := acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(it.ID), Name: commandName(it.Source), Status: endStatus(it.Status)}
	meta := map[string]any{}
	if out := deref(it.AggregatedOutput); out != "" && !streamed {
		meta["terminal_output_delta"] = map[string]any{"data": out, "terminal_id": it.ID}
	}
	if terminal {
		meta["terminal_exit"] = map[string]any{"exit_code": it.ExitCode, "signal": nil, "terminal_id": it.ID}
	} else {
		u.RawOutput = raw(map[string]any{"exit_code": it.ExitCode})
	}
	if len(meta) > 0 {
		u.Meta = meta
	}
	return callUpdate(u)
}

// progress is a running tool's output: a command's, or what was typed
// into it, as a piece of terminal output; an MCP call's progress text.
func (t *tools) progress(method string, d appserver.Delta) (acp.SessionUpdate, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	var meta map[string]any
	switch method {
	case "item/commandExecution/outputDelta":
		if d.Delta != "" {
			t.streamed[d.ItemID] = true
		}
		meta = map[string]any{"terminal_output_delta": map[string]any{"data": d.Delta, "terminal_id": d.ItemID}}
	case "item/commandExecution/terminalInteraction":
		meta = map[string]any{"terminal_output_delta": map[string]any{"data": "\n" + d.Stdin + "\n", "terminal_id": d.ItemID}}
	case "item/mcpToolCall/progress":
		meta = map[string]any{"mcp_output_delta": map[string]any{"data": strings.TrimSpace(d.Message)}}
	default:
		return acp.SessionUpdate{}, false
	}
	return callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(d.ItemID), Meta: meta}), true
}

// action is a command's one action, or unknown: several actions (a pipe)
// or none run as a terminal.
func action(it appserver.Item) appserver.CommandAction {
	if len(it.CommandActions) == 1 {
		return it.CommandActions[0]
	}
	return appserver.CommandAction{Type: "unknown", Command: it.Command}
}

// commandName is the model's tool a command's source names, if one.
func commandName(source string) string {
	switch source {
	case "unifiedExecStartup":
		return "exec_command"
	case "unifiedExecInteraction":
		return "write_stdin"
	}
	return ""
}

var shellPrefix = regexp.MustCompile(`^(?:/bin/)?(?:bash|zsh|sh)\s+(?:-[lc]+\s+)?`)

// stripShell is a command without the shell Codex wraps it in:
// `/bin/zsh -lc 'ls'` is `ls`.
func stripShell(command string) string {
	s := shellPrefix.ReplaceAllString(command, "")
	if len(s) >= 2 && strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") {
		return s[1 : len(s)-1]
	}
	return s
}

func searchFor(query, path string) string {
	switch {
	case query != "" && path != "":
		return "Search for '" + query + "' in " + path
	case query != "":
		return "Search for '" + query + "'"
	case path != "":
		return "Search in '" + path + "'"
	}
	return "Search"
}

// searchTitle is a web search's title by what it did.
func searchTitle(it appserver.Item) string {
	a := it.SearchAction()
	if a == nil {
		if it.Query != "" {
			return "Web search: " + it.Query
		}
		return "Web search"
	}
	switch a.Type {
	case "search":
		q := deref(a.Query)
		if q == "" {
			var qs []string
			for _, x := range a.Queries {
				if x != "" {
					qs = append(qs, x)
				}
			}
			q = strings.Join(qs, ", ")
		}
		if q == "" {
			q = it.Query
		}
		if q != "" {
			return "Web search: " + q
		}
	case "openPage":
		if u := deref(a.URL); u != "" {
			return "Open page: " + u
		}
		return "Open page"
	case "findInPage":
		title := "Find in page"
		if p := deref(a.Pattern); p != "" {
			title += " for '" + p + "'"
		}
		if u := deref(a.URL); u != "" {
			title += " in " + u
		}
		return title
	}
	return "Web search"
}

// mcpOutput is an MCP call's result and error, once it has either.
func mcpOutput(it appserver.Item) json.RawMessage {
	if isNull(it.Result) && isNull(it.Error) {
		return nil
	}
	return raw(map[string]any{"result": orNull(it.Result), "error": orNull(it.Error)})
}

func isNull(r json.RawMessage) bool { return len(r) == 0 || string(r) == "null" }

func orNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// status is an item's status as a tool call's.
func status(s string) acp.ToolCallStatus {
	switch s {
	case "inProgress":
		return "in_progress"
	case "completed":
		return "completed"
	}
	return "failed"
}

// endStatus is an ended item's: completed, else failed.
func endStatus(s string) acp.ToolCallStatus {
	if s == "completed" {
		return "completed"
	}
	return "failed"
}

func call(c acp.ToolCall) acp.SessionUpdate {
	return acp.SessionUpdate{SessionUpdate: "tool_call", ToolCall: &c}
}

func callUpdate(u acp.ToolCallUpdate) acp.SessionUpdate {
	return acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallUpdate: &u}
}

func content(b acp.ContentBlock) acp.ToolCallContent {
	return acp.ToolCallContent{Type: "content", Content: &acp.Content{Content: b}}
}

// raw is v as JSON: the maps of strings, numbers and JSON the agent
// builds always encode.
func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v) //nolint:errchkjson // see above
	return b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
