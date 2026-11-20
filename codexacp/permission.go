package codexacp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// asked answers the app-server's request of the session's thread: an
// approval is put to the user as a permission request, as codex-acp puts
// it. The turn's cancel withdraws it, and Codex is told cancel.
func (s *session) asked(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var of struct {
		TurnID string `json:"turnId"`
	}
	if json.Unmarshal(params, &of) == nil && s.interrupted(of.TurnID) {
		return refusal(method), nil // a turn cancelled: nothing of it is asked, as codex-acp
	}
	if t := s.running(); t != nil {
		var stop context.CancelFunc
		ctx, stop = context.WithCancel(ctx)
		defer stop()
		go func() {
			select {
			case <-t.ctx.Done():
				stop()
			case <-ctx.Done():
			}
		}()
	}
	switch method {
	case "item/commandExecution/requestApproval":
		var p appserver.CommandApproval
		if !s.decode(method, params, &p) {
			return refusal(method), nil
		}
		return s.approveCommand(ctx, p), nil
	case "item/fileChange/requestApproval":
		var p appserver.FileChangeApproval
		if !s.decode(method, params, &p) {
			return refusal(method), nil
		}
		return s.approveFileChange(ctx, p), nil
	case "item/tool/requestUserInput":
		var q question
		if !s.decode(method, params, &q) {
			return refusal(method), nil
		}
		return s.askQuestions(ctx, q), nil
	case "mcpServer/elicitation/request":
		var e mcpElicitation
		if !s.decode(method, params, &e) {
			return refusal(method), nil
		}
		return s.askMCP(ctx, e), nil
	}
	s.a.log.Info("codex request not handled", "method", method)
	return refusal(method), nil
}

// offer is a permission option and the decision it gives Codex.
type offer struct {
	option   acp.PermissionOption
	decision any // a string, or the decision's object as Codex sent it
}

// ask puts the request to the user and gives the decision of the option
// picked; cancel when the user cancelled, or it couldn't be asked.
func (s *session) ask(ctx context.Context, call acp.ToolCallUpdate, offers []offer) appserver.Decision {
	opts := make([]acp.PermissionOption, len(offers))
	for i, o := range offers {
		opts[i] = o.option
	}
	resp, err := s.a.client.RequestPermission(ctx, &acp.RequestPermissionRequest{SessionID: acp.SessionID(s.id), ToolCall: call, Options: opts})
	if err != nil || resp.Outcome.Selected == nil {
		if err != nil {
			s.a.log.Debug("permission not asked", "session", s.id, "err", err)
		}
		return appserver.Decision{Decision: "cancel"}
	}
	for _, o := range offers {
		if o.option.OptionID == resp.Outcome.Selected.OptionID {
			return appserver.Decision{Decision: o.decision}
		}
	}
	return appserver.Decision{Decision: "cancel"}
}

// approveCommand asks whether a command may run, offering the decisions
// Codex lists (or its defaults), allowing first.
func (s *session) approveCommand(ctx context.Context, p appserver.CommandApproval) appserver.Decision {
	offers, ok := commandOffers(p)
	if !ok {
		s.a.log.Warn("command approval cancelled: its decisions don't make a choice", "session", s.id)
		return appserver.Decision{Decision: "cancel"}
	}
	s.tools.mu.Lock()
	s.tools.init()
	source, started := s.tools.sources[p.ItemID]
	s.tools.mu.Unlock()
	call := acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(p.ItemID), Kind: "execute", Status: "pending", Title: permissionTitle(p.CommandActions)}
	if started {
		call.Name = commandName(source)
	}
	input := map[string]any{}
	if c := deref(p.Command); c != "" {
		input["command"] = stripShell(c)
	}
	if c := deref(p.Cwd); c != "" {
		input["cwd"] = c
	}
	if n := p.NetworkApprovalContext; n != nil {
		call.Title = n.Protocol + " network access to " + n.Host
		call.Content = []acp.ToolCallContent{content(textBlock(n.Protocol + " access to " + n.Host))}
		if n.Protocol == "http" || n.Protocol == "https" {
			input["url"] = n.Protocol + "://" + n.Host
		}
	}
	if !isNull(p.AdditionalPermissions) {
		input["additionalPermissions"] = p.AdditionalPermissions
	}
	if len(input) > 0 {
		call.RawInput = raw(input)
	}
	for _, path := range actionPaths(p.CommandActions) {
		call.Locations = append(call.Locations, acp.ToolCallLocation{Path: path})
	}
	return s.ask(ctx, call, offers)
}

// approveFileChange asks whether Codex may write the files of a change.
func (s *session) approveFileChange(ctx context.Context, p appserver.FileChangeApproval) appserver.Decision {
	s.tools.mu.Lock()
	s.tools.init()
	item := s.tools.changes[p.ItemID]
	s.tools.mu.Unlock()
	call := acp.ToolCallUpdate{ToolCallID: acp.ToolCallID(p.ItemID), Kind: "edit", Status: "pending", Title: "Edit files"}
	var seen []string
	for _, c := range item.Changes {
		if !slices.Contains(seen, c.Path) {
			seen = append(seen, c.Path)
			call.Locations = append(call.Locations, acp.ToolCallLocation{Path: c.Path})
		}
	}
	return s.ask(ctx, call, []offer{
		{permission("allow_once", "Yes, proceed", "allow_once"), "accept"},
		{permission("allow_for_session", "Yes, and don't ask again for these files", "allow_always"), "acceptForSession"},
		{permission("cancel", "No, and tell Codex what to do differently", "reject_once"), "cancel"},
	})
}

func permission(id, name, kind string) acp.PermissionOption {
	return acp.PermissionOption{OptionID: acp.PermissionOptionID(id), Name: name, Kind: acp.PermissionOptionKind(kind)}
}

// commandOffers are a command approval's options, from its decisions:
// allowing ones first, then refusing ones; false when they don't make a
// choice (none allows or none refuses) or one is unknown.
func commandOffers(p appserver.CommandApproval) ([]offer, bool) {
	network := p.NetworkApprovalContext != nil
	var offers []offer
	amendments := 0
	for _, d := range decisions(p) {
		var name string
		if err := json.Unmarshal(d, &name); err == nil {
			o, ok := namedOffer(name, p)
			if !ok {
				return nil, false
			}
			offers = append(offers, o)
			continue
		}
		var obj struct {
			Exec *struct {
				Amendment []string `json:"execpolicy_amendment"`
			} `json:"acceptWithExecpolicyAmendment"`
			Network *struct {
				Amendment struct {
					Host   string `json:"host"`
					Action string `json:"action"`
				} `json:"network_policy_amendment"`
			} `json:"applyNetworkPolicyAmendment"`
		}
		if err := json.Unmarshal(d, &obj); err != nil {
			return nil, false
		}
		var whole any
		_ = json.Unmarshal(d, &whole)
		switch {
		case obj.Exec != nil && len(obj.Exec.Amendment) > 0:
			// Only the rule Codex proposed, which the request shows, is
			// offered.
			prefix := execPrefix(obj.Exec.Amendment)
			if !slices.Equal(obj.Exec.Amendment, p.ProposedExecpolicyAmendment) || strings.ContainsAny(prefix, "\r\n") {
				continue
			}
			offers = append(offers, offer{permission("accept_execpolicy_amendment", "Yes, and don't ask again for commands that start with "+codeSpan(prefix), "allow_always"), whole})
		case obj.Network != nil:
			// Only a rule for the host the request shows, with an action
			// the schema knows, is offered: the user can't see another.
			a := obj.Network.Amendment
			if !network || a.Host != p.NetworkApprovalContext.Host || (a.Action != "allow" && a.Action != "deny") || !proposedNetwork(p, a.Host, a.Action) {
				continue
			}
			label, kind := "No, and block this host in the future", "reject_always"
			if a.Action == "allow" {
				label, kind = "Yes, and allow this host in the future", "allow_always"
			}
			offers = append(offers, offer{permission("apply_network_policy_amendment:"+strconv.Itoa(amendments), label, kind), whole})
			amendments++
		default:
			return nil, false
		}
	}
	slices.SortStableFunc(offers, func(a, b offer) int { return order(a.option.Kind) - order(b.option.Kind) })
	allows := slices.ContainsFunc(offers, func(o offer) bool { return order(o.option.Kind) < 2 })
	refuses := slices.ContainsFunc(offers, func(o offer) bool { return order(o.option.Kind) == 2 })
	// Two options with one id would give the first's decision for
	// either: such a request is refused, as codex-acp refuses it.
	ids := map[acp.PermissionOptionID]bool{}
	for _, o := range offers {
		if ids[o.option.OptionID] {
			return nil, false
		}
		ids[o.option.OptionID] = true
	}
	return offers, allows && refuses
}

// proposedNetwork says Codex proposed the network rule for host and
// action.
func proposedNetwork(p appserver.CommandApproval, host, action string) bool {
	for _, r := range p.ProposedNetworkPolicyAmendments {
		var a struct {
			Host   string `json:"host"`
			Action string `json:"action"`
		}
		if json.Unmarshal(r, &a) == nil && a.Host == host && a.Action == action {
			return true
		}
	}
	return false
}

// namedOffer is the option of a decision Codex names by a word; false
// for a word it doesn't have.
func namedOffer(name string, p appserver.CommandApproval) (offer, bool) {
	network := p.NetworkApprovalContext != nil
	switch name {
	case "accept":
		label := "Yes, proceed"
		if network {
			label = "Yes, just this once"
		}
		return offer{permission("allow_once", label, "allow_once"), name}, true
	case "acceptForSession":
		label := "Yes, and don't ask again for this command in this session"
		switch {
		case network:
			label = "Yes, and allow this host for this conversation"
		case !isNull(p.AdditionalPermissions):
			label = "Yes, and allow these permissions for this session"
		}
		return offer{permission("allow_for_session", label, "allow_always"), name}, true
	case "decline":
		return offer{permission("decline", "No, continue without running it", "reject_once"), name}, true
	case "cancel":
		return offer{permission("cancel", "No, and tell Codex what to do differently", "reject_once"), name}, true
	}
	return offer{}, false
}

// decisions are the approval's decisions, or Codex's defaults when it
// lists none.
func decisions(p appserver.CommandApproval) []json.RawMessage {
	if p.AvailableDecisions != nil {
		return p.AvailableDecisions
	}
	str := func(s string) json.RawMessage { return raw(s) }
	switch {
	case p.NetworkApprovalContext != nil:
		out := []json.RawMessage{str("accept"), str("acceptForSession")}
		for _, a := range p.ProposedNetworkPolicyAmendments {
			out = append(out, raw(map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": a}}))
		}
		return append(out, str("decline"), str("cancel"))
	case !isNull(p.AdditionalPermissions):
		return []json.RawMessage{str("accept"), str("cancel")}
	}
	out := []json.RawMessage{str("accept"), str("acceptForSession")}
	if len(p.ProposedExecpolicyAmendment) > 0 {
		out = append(out, raw(map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": p.ProposedExecpolicyAmendment}}))
	}
	return append(out, str("decline"), str("cancel"))
}

func order(kind acp.PermissionOptionKind) int {
	switch kind {
	case "allow_once":
		return 0
	case "allow_always":
		return 1
	default:
		return 2
	}
}

// execPrefix is the command prefix an amendment allows, as a user would
// type it: a shell's -c script as it is when the script is the last word
// (the amendment covers every word, so a word after it must show), other
// words quoted as a shell would need them.
func execPrefix(argv []string) string {
	exe := strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
	if (exe == "bash" || exe == "zsh" || exe == "sh") && len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	if exe == "pwsh" || exe == "powershell" {
	flags:
		for i := 1; i+1 < len(argv); i++ {
			switch strings.ToLower(argv[i]) {
			case "-command", "-c":
				if i+2 == len(argv) {
					return argv[i+1]
				}
				break flags
			case "-nologo", "-noprofile":
			default:
				break flags
			}
		}
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// codeSpan is s as a Markdown code span: fenced by one more backtick
// than its longest run of them, and padded when it starts or ends with
// one, so a backtick in s can't close the span early.
func codeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// permissionTitle is a command approval's title by its first action.
func permissionTitle(actions []appserver.CommandAction) string {
	if len(actions) == 0 {
		return "Run command"
	}
	switch actions[0].Type {
	case "read":
		if len(actions) == 1 {
			return "Read file"
		}
		return "Run command with file reads"
	case "listFiles":
		return "List files"
	case "search":
		return "Search files"
	}
	return "Run command"
}

// actionPaths are the files and folders a command's actions name.
func actionPaths(actions []appserver.CommandAction) []string {
	var out []string
	for _, a := range actions {
		if p := deref(a.Path); p != "" && a.Type != "unknown" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}
