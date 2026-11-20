package codexacp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/afrase/acp-go/unstable"
)

// What Codex asks of the user besides approvals, as codex-acp asks it
// (its CodexElicitationHandler): a plan-mode question as a form, an MCP
// server's elicitation as a form, or, for a tool's approval (a form with
// no fields) or a page to open, as a permission request.

// question is item/tool/requestUserInput's params.
type question struct {
	ThreadID         string `json:"threadId"`
	ItemID           string `json:"itemId"`
	AutoResolutionMs *int64 `json:"autoResolutionMs"`
	Questions        []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		IsOther  bool   `json:"isOther"`
		IsSecret bool   `json:"isSecret"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// otherOption is the choice a question with a free answer adds, and
// notePrefix what a note's answer starts with, as codex-acp has them.
const (
	otherOption = "None of the above"
	notePrefix  = "user_note: "
)

// elicitation is the client's answer to elicitation/create.
type elicitation struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content"`
	Meta    map[string]any `json:"_meta"`
}

// askQuestions puts Codex's questions to the user as one form: each a
// required string, with its options as choices (and "None of the above"
// when it takes another answer, then an optional `<id>_note`), and gives
// Codex the answers: the choice, then the note as "user_note: …". A
// client without forms, or one that doesn't accept, answers nothing.
func (s *session) askQuestions(ctx context.Context, q question) any {
	none := map[string]any{"answers": map[string]any{}}
	s.a.mu.Lock()
	forms := s.a.forms
	s.a.mu.Unlock()
	if !forms {
		return none
	}
	props, required, notes := q.form()
	req := map[string]any{"sessionId": s.id, "toolCallId": q.ItemID, "mode": "form", "message": "Codex needs your input to continue.",
		"requestedSchema": map[string]any{"type": "object", "properties": ordered(props), "required": required},
		"_meta":           map[string]any{"codex": map[string]any{"autoResolutionMs": q.AutoResolutionMs}}}
	var resp elicitation
	if err := s.a.client.Call(ctx, "elicitation/create", req, &resp); err != nil || resp.Action != "accept" {
		return none
	}
	answers := map[string]any{}
	for _, x := range q.Questions {
		got := values(resp.Content[x.ID])
		if note, ok := notes[x.ID]; ok {
			for _, n := range values(resp.Content[note]) {
				got = append(got, notePrefix+strings.TrimSpace(n))
			}
		}
		if len(got) > 0 {
			answers[x.ID] = map[string]any{"answers": got}
		}
	}
	return map[string]any{"answers": answers}
}

// form is the questions' form: its fields in order (name, schema pairs),
// those required, and each question's note field by its id.
func (q question) form() (props []any, required []string, notes map[string]string) {
	ids := map[string]bool{}
	for _, x := range q.Questions {
		ids[x.ID] = true
	}
	notes = map[string]string{}
	for _, x := range q.Questions {
		title := x.Question
		if title == "" {
			title = x.Header
		}
		if title == "" {
			title = x.ID
		}
		p := map[string]any{"type": "string", "title": title, "_meta": map[string]any{"codex": map[string]any{"isOther": x.IsOther, "isSecret": x.IsSecret}}}
		if x.Header != "" {
			p["description"] = x.Header
		}
		other := x.IsOther && len(x.Options) > 0
		if len(x.Options) > 0 {
			var choices []map[string]any
			hasOther := false
			for _, o := range x.Options {
				c := map[string]any{"const": o.Label, "title": o.Label}
				if o.Description != "" {
					c["description"] = o.Description
				}
				choices = append(choices, c)
				hasOther = hasOther || o.Label == otherOption
			}
			if other && !hasOther {
				choices = append(choices, map[string]any{"const": otherOption, "title": otherOption, "description": "Provide a different answer in the note field."})
			}
			p["oneOf"] = choices
		}
		required = append(required, x.ID)
		props = append(props, x.ID, p)
		if other {
			note := noteField(x.ID, ids)
			notes[x.ID] = note
			props = append(props, note, map[string]any{"type": "string", "title": "Additional answer or note",
				"_meta": map[string]any{"codex": map[string]any{"questionId": x.ID, "role": "user_note", "isSecret": x.IsSecret}}})
		}
	}
	return props, required, notes
}

// noteField is a question's note's field: `<id>_note`, numbered past any
// question's id it would take.
func noteField(id string, ids map[string]bool) string {
	base := id + "_note"
	f := base
	for i := 1; ids[f]; i++ {
		f = fmt.Sprintf("%s%d", base, i)
	}
	return f
}

// values is an answer's value as strings: none for an empty one.
func values(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return []string{fmt.Sprint(v)}
}

// orderedObject is a JSON object whose keys keep their order: a form's
// fields are shown in it.
type orderedObject []any // key, value, key, value…

func ordered(pairs []any) orderedObject { return orderedObject(pairs) }

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(o); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(o[i])
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(o[i+1])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// mcpElicitation is mcpServer/elicitation/request's params.
type mcpElicitation struct {
	ThreadID        string          `json:"threadId"`
	ServerName      string          `json:"serverName"`
	Mode            string          `json:"mode"` // form, url, openai/form, openaiForm
	Meta            map[string]any  `json:"_meta"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	URL             string          `json:"url"`
	ElicitationID   string          `json:"elicitationId"`
}

// messageOnly says the elicitation is a form with no fields: a yes or no.
func (e mcpElicitation) messageOnly() bool {
	if e.Mode != "form" && e.Mode != "openai/form" && e.Mode != "openaiForm" {
		return false
	}
	if isNull(e.RequestedSchema) {
		return true
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	return json.Unmarshal(e.RequestedSchema, &schema) == nil && schema.Type == "object" && schema.Properties != nil && len(schema.Properties) == 0
}

// persists is what the elicitation's _meta.persist lets an approval keep:
// "session", "always".
func (e mcpElicitation) persists() map[string]bool {
	out := map[string]bool{}
	switch p := e.Meta["persist"].(type) {
	case string:
		out[p] = true
	case []any:
		for _, x := range p {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

var cancelled = map[string]any{"action": "cancel", "content": nil, "_meta": nil}

// askMCP puts an MCP server's elicitation to the user. A tool's approval
// (Codex marks it codex_approval_kind mcp_tool_call, a form with no
// fields) is a permission request about the tool's call, when Codex has
// one call of that server running, with Allow, Allow for this session or
// Always allow as the server lets it keep, and Cancel; a form with fields
// is a form; a page to open is a permission request. The answer is
// accept (with what to keep), decline or cancel.
func (s *session) askMCP(ctx context.Context, e mcpElicitation) any {
	approval := e.Meta["codex_approval_kind"] == "mcp_tool_call" && e.messageOnly()
	persist := e.persists()
	var callID string
	if approval {
		callID = s.tools.pendingMCP(e.ServerName)
	}
	s.a.mu.Lock()
	forms := s.a.forms
	s.a.mu.Unlock()
	if !e.messageOnly() && e.Mode == "form" {
		if !forms {
			return cancelled
		}
		return s.mcpForm(ctx, e, callID)
	}
	if !e.messageOnly() && e.Mode != "url" {
		return cancelled
	}
	call, standalone := s.mcpCall(e, approval, callID)
	var meta map[string]any
	if approval && e.Mode != "url" {
		meta = map[string]any{"is_mcp_tool_approval": true}
	}
	opts := []acp.PermissionOption{permission("allow_once", "Allow", "allow_once")}
	if !approval {
		opts[0].OptionID = "accept"
	}
	if persist["session"] {
		opts = append(opts, permission("allow_session", "Allow for this session", "allow_always"))
	}
	if persist["always"] {
		opts = append(opts, permission("allow_always", "Always allow", "allow_always"))
	}
	if !approval {
		opts = append(opts, permission("decline", "Deny", "reject_once"))
	}
	opts = append(opts, permission("cancel", "Cancel", "reject_once"))
	resp, err := s.a.client.RequestPermission(ctx, &acp.RequestPermissionRequest{SessionID: acp.SessionID(s.id), ToolCall: call, Options: opts, Meta: meta})
	answer := cancelled
	if err == nil && resp.Outcome.Selected != nil {
		answer = mcpAnswer(string(resp.Outcome.Selected.OptionID), approval, persist)
	}
	switch {
	case standalone:
		s.update(callUpdate(acp.ToolCallUpdate{ToolCallID: call.ToolCallID, Status: "completed", RawOutput: raw(map[string]any{"action": answer["action"]})}))
	case callID != "" && answer["action"] == "accept":
		s.update(callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(callID), Status: "in_progress"}))
	}
	return answer
}

// mcpCall is the tool call an MCP permission request is about: the tool's
// own, else one of its own (standalone).
func (s *session) mcpCall(e mcpElicitation, approval bool, callID string) (acp.ToolCallUpdate, bool) {
	if e.Mode == "url" {
		return acp.ToolCallUpdate{ToolCallID: acp.ToolCallID("elicitation-" + e.ElicitationID), Kind: "fetch", Status: "pending",
			Title: "MCP server requests to open a URL", RawInput: raw(map[string]any{"serverName": e.ServerName, "description": e.Message, "url": e.URL}),
			Content: []acp.ToolCallContent{content(textBlock(e.Message))}}, true
	}
	if callID != "" {
		return acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(callID), Kind: "execute", Status: "pending"}, false
	}
	kind, title := "other", "Question from MCP server"
	if approval {
		kind, title = "execute", "MCP tool call approval"
	}
	s.mu.Lock()
	s.asks++
	id := fmt.Sprintf("elicitation:%s:%s:%d", s.id, e.ServerName, s.asks)
	s.mu.Unlock()
	return acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(id), Kind: acp.ToolKind(kind), Status: "pending", Title: title,
		RawInput: raw(map[string]any{"serverName": e.ServerName, "description": e.Message, "schema": orNull(e.RequestedSchema)}),
		Content:  []acp.ToolCallContent{content(textBlock(e.Message))}}, true
}

// mcpAnswer is Codex's answer for the option picked; one the request
// didn't offer is cancel.
func mcpAnswer(option string, approval bool, persist map[string]bool) map[string]any {
	accept := func(meta any) map[string]any {
		return map[string]any{"action": "accept", "content": nil, "_meta": meta}
	}
	switch {
	case option == "allow_session" && persist["session"]:
		return accept(map[string]any{"persist": "session"})
	case option == "allow_always" && persist["always"]:
		return accept(map[string]any{"persist": "always"})
	case option == "allow_once" && approval, option == "accept" && !approval:
		return accept(nil)
	case option == "decline" && !approval:
		return map[string]any{"action": "decline", "content": nil, "_meta": nil}
	}
	return cancelled
}

// mcpForm asks an MCP server's form of the user, its schema as ACP takes
// it (an enum with enumNames as oneOf choices).
func (s *session) mcpForm(ctx context.Context, e mcpElicitation, callID string) any {
	var schema any
	_ = json.Unmarshal(e.RequestedSchema, &schema)
	normal, _ := normalizeSchema(schema).(map[string]any)
	if normal == nil {
		normal = map[string]any{"properties": map[string]any{}}
	}
	normal["type"] = "object"
	req := map[string]any{"sessionId": s.id, "mode": "form", "message": e.Message, "requestedSchema": normal}
	if callID != "" {
		req["toolCallId"] = callID
	}
	if e.Meta != nil {
		req["_meta"] = e.Meta
	}
	var resp elicitation
	if err := s.a.client.Call(ctx, "elicitation/create", req, &resp); err != nil {
		return cancelled
	}
	meta := any(nil)
	if len(resp.Meta) > 0 {
		meta = resp.Meta
	}
	switch resp.Action {
	case "accept":
		content := any(nil)
		if len(resp.Content) > 0 {
			content = resp.Content
		}
		if callID != "" {
			s.update(callUpdate(acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(callID), Status: "in_progress"}))
		}
		return map[string]any{"action": "accept", "content": content, "_meta": meta}
	case "decline":
		return map[string]any{"action": "decline", "content": nil, "_meta": meta}
	}
	return map[string]any{"action": "cancel", "content": nil, "_meta": meta}
}

// normalizeSchema turns a string's enum and enumNames into oneOf choices,
// at any depth.
func normalizeSchema(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeSchema(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = normalizeSchema(e)
		}
		enum, ok1 := out["enum"].([]any)
		names, ok2 := out["enumNames"].([]any)
		if _, has := out["oneOf"]; out["type"] == "string" && ok1 && ok2 && !has {
			var choices []any
			for i, e := range enum {
				title := fmt.Sprint(e)
				if i < len(names) {
					title = fmt.Sprint(names[i])
				}
				choices = append(choices, map[string]any{"const": fmt.Sprint(e), "title": title})
			}
			out["oneOf"] = choices
			delete(out, "enum")
			delete(out, "enumNames")
		}
		return out
	}
	return v
}
