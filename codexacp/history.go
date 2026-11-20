package codexacp

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// replay is the updates an item of a thread's history makes on a load,
// as codex-acp replays it: the user's message and the agent's under
// their item ids, thoughts, and each tool call as it ended. Permission
// requests and questions aren't in the history.
func replay(it appserver.Item) []acp.SessionUpdate {
	var h tools
	switch it.Type {
	case "userMessage":
		var out []acp.SessionUpdate
		for _, in := range it.Inputs {
			if b, ok := inputBlock(in); ok {
				c := &acp.ContentChunk{Content: b, MessageID: acp.MessageID(it.ID)}
				out = append(out, acp.SessionUpdate{SessionUpdate: "user_message_chunk", UserMessageChunk: c})
			}
		}
		return out
	case "agentMessage":
		return []acp.SessionUpdate{chunk("agent_message_chunk", it.ID, it.Text)}
	case "plan":
		if it.Text == "" {
			return nil
		}
		return []acp.SessionUpdate{chunk("agent_message_chunk", it.ID, it.Text)}
	case "reasoning":
		parts := it.Summary
		if len(parts) == 0 {
			parts = it.ReasoningContent
		}
		out := make([]acp.SessionUpdate, 0, len(parts))
		for _, p := range parts {
			out = append(out, chunk("agent_thought_chunk", it.ID, p))
		}
		return out
	case "enteredReviewMode", "exitedReviewMode":
		verb := "Entered"
		if it.Type == "exitedReviewMode" {
			verb = "Exited"
		}
		return []acp.SessionUpdate{chunk("agent_message_chunk", "", verb+" review mode: "+it.Review)}
	case "commandExecution":
		out := h.item(true, it)
		if it.Status != "inProgress" {
			end := h.item(false, it)
			end[0].ToolCallUpdate.Name = "" // codex-acp names a command's end only live
			out = append(out, end...)
		}
		return out
	case "webSearch":
		return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: searchTitle(it), Kind: "search", Status: "completed",
			RawInput: raw(map[string]any{"query": it.Query, "action": orNull(it.Action)})})}
	case "contextCompaction":
		return []acp.SessionUpdate{call(acp.ToolCall{ToolCallID: acp.ToolCallID(it.ID), Title: "Compact conversation", Kind: "think", Status: "completed"})}
	case "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "imageView":
		return h.item(true, it)
	}
	return nil
}

// inputBlock is a piece of the user's message as ACP content: text, an
// image as a link to its URL, a file as a link to it, a skill by name.
func inputBlock(in appserver.UserInput) (acp.ContentBlock, bool) {
	switch in.Type {
	case "text":
		return textBlock(in.Text), in.Text != ""
	case "image":
		return textBlock(link("image", in.URL)), true
	case "localImage", "mention":
		name := in.Name
		if in.Type != "mention" || strings.TrimSpace(name) == "" {
			name = filepath.Base(in.Path)
		}
		if uri := fileURI(in.Path); uri != "" {
			return acp.ContentBlock{Type: "resource_link", ResourceLink: &acp.ResourceLink{Name: name, URI: uri}}, true
		}
		return textBlock(link(name, in.Path)), true
	case "skill":
		return textBlock("skill:" + in.Name + " (" + in.Path + ")"), true
	}
	return acp.ContentBlock{}, false
}

// fileURI is a local path as a file: URI, or "" for one that isn't
// absolute.
func fileURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	p := filepath.ToSlash(path)
	if len(p) > 2 && p[1] == ':' { // C:/…
		p = "/" + p
	}
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// maxTitle is the longest title codex-acp sends, in UTF-16 units; this
// counts characters.
const maxTitle = 256

// title is text as a session's title: on one line, cut at maxTitle with
// an ellipsis; "" for text with nothing in it.
func title(text string) string {
	t := collapse(text)
	if utf8.RuneCountInString(t) <= maxTitle {
		return t
	}
	r := []rune(t)[:maxTitle-1]
	return strings.TrimRight(string(r), " ") + "…"
}
