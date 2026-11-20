package codexacp

import (
	"encoding/json"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// notified takes a notification of the session's thread, as codex-acp
// renders it for a client that is neither JetBrains AIR nor declares
// notices: text and thoughts as chunks whose message id is Codex's item
// id, tools as tool calls, warnings as agent text, and Codex's to-do plan
// as a plan update.
func (s *session) notified(method string, params json.RawMessage) {
	switch method {
	case "turn/started", "turn/completed":
		var n appserver.TurnNotification
		if !s.decode(method, params, &n) {
			// The turn's id alone still starts or ends the prompt's turn.
			var id struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(params, &id) != nil || id.Turn.ID == "" {
				return
			}
			n.Turn = appserver.Turn{ID: id.Turn.ID}
		}
		if method == "turn/completed" {
			s.tools.turnEnded()
			s.completed(n.Turn)
		} else if t := s.running(); t != nil {
			s.started(t, n.Turn.ID)
		}
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/reasoning/summaryPartAdded":
		s.text(method, params)
	case "item/started", "item/completed":
		var n appserver.ItemNotification
		if !s.decode(method, params, &n) {
			return
		}
		it, ok := s.item(method, n.Item)
		if !ok {
			return
		}
		s.a.discover(s.id, s.id, it)
		if method == "item/started" && it.Type != "userMessage" {
			s.modelOutput()
		}
		for _, u := range s.tools.item(method == "item/started", it) {
			s.update(u)
		}
	case "item/commandExecution/outputDelta", "item/commandExecution/terminalInteraction", "item/mcpToolCall/progress":
		var d appserver.Delta
		if s.decode(method, params, &d) {
			if u, ok := s.tools.progress(method, d); ok {
				s.update(u)
			}
		}
	case "thread/tokenUsage/updated":
		s.tokenUsage(params)
	case "thread/name/updated", "thread/status/changed":
		s.info(method, params)
	case "turn/plan/updated":
		s.plan(params)
	case "error":
		s.turnError(params)
	case "warning", "configWarning", "model/rerouted":
		s.unthreaded(method, params)
	}
}

// childNotified takes a notification of one of the session's subagents'
// threads, as codex-acp takes it for a client that doesn't take subagent
// sessions: nothing is shown, but its items are noted for the approvals
// it asks for, which come to this session, and for the subagents it
// spawns in turn.
func (s *session) childNotified(thread, method string, params json.RawMessage) {
	if method != "item/started" && method != "item/completed" {
		return
	}
	var n appserver.ItemNotification
	if !s.decode(method, params, &n) {
		return
	}
	it, ok := s.item(method, n.Item)
	if !ok {
		return
	}
	s.a.discover(s.id, thread, it)
	s.tools.observe(method == "item/started", it)
}

// text takes a piece of the agent's message or of its reasoning; a new
// part of a reasoning's summary starts after a blank line.
func (s *session) text(method string, params json.RawMessage) {
	var d appserver.Delta
	if !s.decode(method, params, &d) {
		return
	}
	switch method {
	case "item/agentMessage/delta":
		s.update(chunk("agent_message_chunk", d.ItemID, d.Delta))
		return
	case "item/reasoning/summaryPartAdded":
		d.Delta = "\n\n"
	}
	s.tools.thought(d.ItemID)
	s.update(chunk("agent_thought_chunk", d.ItemID, d.Delta))
}

// info takes the thread's name (its title; null when it has none) and
// status (in _meta.codex.threadStatus, as codex-acp sends it).
func (s *session) info(method string, params json.RawMessage) {
	info := &acp.SessionInfoUpdate{}
	if method == "thread/status/changed" {
		var n struct {
			Status json.RawMessage `json:"status"`
		}
		if !s.decode(method, params, &n) {
			return
		}
		info.Meta = map[string]any{"codex": map[string]any{"threadStatus": n.Status}}
	} else {
		var n appserver.ThreadNameNotification
		if !s.decode(method, params, &n) {
			return
		}
		info.Title = acp.Nullable[string]{Present: true} // null: no name
		if n.ThreadName != nil {
			if c := title(*n.ThreadName); c != "" {
				info.Title.Value = &c
			}
		}
		s.mu.Lock()
		s.titled = info.Title.Value != nil
		s.mu.Unlock()
	}
	s.update(acp.SessionUpdate{SessionUpdate: "session_info_update", SessionInfoUpdate: info})
}

// plan takes Codex's to-do plan, as ACP's plan.
func (s *session) plan(params json.RawMessage) {
	var n appserver.PlanNotification
	if !s.decode("turn/plan/updated", params, &n) {
		return
	}
	plan := &acp.Plan{Entries: []acp.PlanEntry{}}
	for _, p := range n.Plan {
		status := p.Status
		if status == "inProgress" {
			status = "in_progress"
		}
		plan.Entries = append(plan.Entries, acp.PlanEntry{Content: p.Step, Priority: "medium", Status: acp.PlanEntryStatus(status)})
	}
	s.update(acp.SessionUpdate{SessionUpdate: "plan", Plan: plan})
}

// unthreaded takes a warning: Codex's own words as agent text with no
// message id, as codex-acp sends them to a client without notices; a
// warning of no thread goes to the sessions with a turn running.
func (s *session) unthreaded(method string, params json.RawMessage) {
	if threadOf(params) == "" && s.running() == nil {
		return
	}
	var w appserver.WarningNotification
	if !s.decode(method, params, &w) {
		return
	}
	switch method {
	case "warning":
		s.update(chunk("agent_message_chunk", "", "Warning: "+w.Message+"\n\n"))
	case "configWarning":
		text := w.Summary
		if w.Details != nil && *w.Details != "" {
			text += "\n\n" + *w.Details
		}
		s.update(chunk("agent_message_chunk", "", "Config warning: "+text+"\n\n"))
	case "model/rerouted":
		var r struct {
			FromModel string `json:"fromModel"`
			ToModel   string `json:"toModel"`
			Reason    string `json:"reason"`
		}
		if s.decode(method, params, &r) {
			s.update(chunk("agent_thought_chunk", "", "Model rerouted from "+r.FromModel+" to "+r.ToModel+" ("+r.Reason+").\n\n"))
		}
	}
}

// turnError takes an error notification. One Codex retries is only
// logged; one that ends the running turn is shown as agent text, and a
// missing login or a spent limit fails the prompt.
func (s *session) turnError(params json.RawMessage) {
	var n appserver.ErrorNotification
	if !s.decode("error", params, &n) {
		return
	}
	t := s.running()
	if n.WillRetry || t == nil {
		s.a.log.Debug("codex error", "session", s.id, "retry", n.WillRetry)
		return
	}
	switch n.Error.ErrorInfo() {
	case "unauthorized":
		t.fail(&acp.Error{Code: acp.ErrorCodeAuthenticationRequired, Message: "Authentication required"})
		return
	case "usageLimitExceeded":
		data := raw(map[string]any{"message": n.Error.Message, "codexErrorInfo": n.Error.CodexErrorInfo})
		t.fail(&acp.Error{Code: acp.ErrorCodeInternalError, Message: "Internal error", Data: data})
	}
	s.update(chunk("agent_message_chunk", "", serviceMessage(n.Error.Message)+"\n\n"))
}

func (t *turn) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failure = err
}

// tokenUsage takes the thread's token counts: the context's use (the last
// request's tokens of the model's window) and the prompt's count.
func (s *session) tokenUsage(params json.RawMessage) {
	var n appserver.TokenUsageNotification
	if !s.decode("thread/tokenUsage/updated", params, &n) {
		return
	}
	total := n.TokenUsage.Total
	s.mu.Lock()
	s.total = &total
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		t.usage.observe(total, n.TokenUsage.Last)
		t.mu.Unlock()
	}
	if w := n.TokenUsage.ModelContextWindow; w != nil && *w > 0 {
		s.update(acp.SessionUpdate{SessionUpdate: "usage_update", UsageUpdate: &acp.UsageUpdate{
			Used: uint64(max(n.TokenUsage.Last.TotalTokens, 0)), Size: uint64(*w)}})
	}
}

// modelOutput notes that the model answered in this prompt, so the next
// token count is one of its requests'.
func (s *session) modelOutput() {
	if t := s.running(); t != nil {
		t.mu.Lock()
		t.usage.output = true
		t.mu.Unlock()
	}
}

// running is the prompt's turn, or nil.
func (s *session) running() *turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func chunk(kind, messageID, text string) acp.SessionUpdate {
	c := &acp.ContentChunk{Content: textBlock(text), MessageID: acp.MessageID(messageID)}
	u := acp.SessionUpdate{SessionUpdate: kind}
	switch kind {
	case "agent_thought_chunk":
		u.AgentThoughtChunk = c
	case "user_message_chunk":
		u.UserMessageChunk = c
	default:
		u.AgentMessageChunk = c
	}
	return u
}

func textBlock(text string) acp.ContentBlock {
	return acp.ContentBlock{Type: "text", Text: &acp.TextContent{Text: text}}
}

// collapse is a title on one line.
func collapse(title string) string { return strings.Join(strings.Fields(title), " ") }

// serviceMessage is a provider's JSON error ({"type":"error","status":
// 4xx|5xx,"error":{"type":…,"message":…}}) as its message, and any other
// text as it is.
func serviceMessage(text string) string {
	var v struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
		Error  *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(text), &v) != nil || v.Type != "error" || v.Status < 400 || v.Status > 599 || v.Error == nil ||
		strings.TrimSpace(v.Error.Message) == "" || v.Error.Type == "" {
		return text
	}
	return v.Error.Message
}

// item decodes an item a message of method carries. One that doesn't
// decode is logged, by its kind and never its content, and skipped.
func (s *session) item(method string, b json.RawMessage) (appserver.Item, bool) {
	it, err := appserver.DecodeItem(b)
	if err != nil {
		s.a.log.Warn("skipped a Codex item that doesn't decode", "method", method, "session", s.id, "type", it.Type, "err", err)
		return it, false
	}
	return it, true
}

// decode decodes the params of a message of method into v, saying
// whether it could; when it can't, that is logged by the method, never
// the content, and the message is dropped.
func (s *session) decode(method string, params json.RawMessage, v any) bool {
	if err := json.Unmarshal(params, v); err != nil {
		s.a.log.Warn("skipped a Codex message that doesn't decode", "method", method, "session", s.id, "err", err)
		return false
	}
	return true
}
