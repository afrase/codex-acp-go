package codexacp

import (
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// mode is one of codex-acp's four modes: an approval policy, who reviews
// approvals, and a sandbox, sent with each turn.
type mode struct {
	id, name, description string
	approval, reviewer    string
	sandbox               map[string]any
}

func workspaceWrite() map[string]any {
	return map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": false,
		"excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
}

// modes are codex-acp's, in its order; "agent" (Auto review) is the
// default.
var modes = []mode{
	{"read-only", "Read-only", "Requires approval to edit files and access the internet.",
		"on-request", "user", map[string]any{"type": "readOnly", "networkAccess": false}},
	{"workspace-write", "Workspace access", "Edit workspace files; ask before writing outside the workspace or accessing the network.",
		"on-request", "user", workspaceWrite()},
	{"agent", "Auto review", "Only ask for actions detected as potentially unsafe",
		"on-request", "auto_review", workspaceWrite()},
	{"agent-full-access", "Full access", "Unrestricted access to the internet and any file on your computer",
		"never", "user", map[string]any{"type": "dangerFullAccess"}},
}

const defaultMode = "agent"

func modeByID(id string) (mode, bool) {
	for _, m := range modes {
		if m.id == id {
			return m, true
		}
	}
	return mode{}, false
}

// The config options' ids, as codex-acp's.
const (
	optMode   = "mode"
	optCollab = "collaboration_mode"
	optModel  = "model"
	optEffort = "reasoning_effort"
)

// settings is what a session's options say: its mode, collaboration
// mode, model and effort, and the models Codex offers.
type settings struct {
	mode   string
	collab string // "default" or "plan"
	model  string
	effort string
	models []appserver.Model
}

// current is the model in use, if Codex lists it.
func (st *settings) current() (appserver.Model, bool) {
	for _, m := range st.models {
		if m.ID == st.model {
			return m, true
		}
	}
	return appserver.Model{}, false
}

// pick sets the model and effort as codex-acp does when a thread opens:
// the thread's model, else the default one; its effort, else the
// model's default, else medium.
func (st *settings) pick(model string, effort *string) {
	st.model = model
	m, listed := st.current()
	if !listed && model == "" {
		for _, d := range st.models {
			if d.IsDefault {
				m, listed, st.model = d, true, d.ID
				break
			}
		}
	}
	switch {
	case effort != nil && *effort != "":
		st.effort = *effort
	case listed && m.DefaultReasoningEffort != "":
		st.effort = m.DefaultReasoningEffort
	default:
		st.effort = "medium"
	}
}

// modeState is the legacy modes, which clients may read beside the
// options.
func (st *settings) modeState() *acp.SessionModeState {
	out := &acp.SessionModeState{CurrentModeID: acp.SessionModeID(st.mode)}
	for _, m := range modes {
		out.AvailableModes = append(out.AvailableModes, acp.SessionMode{ID: acp.SessionModeID(m.id), Name: m.name, Description: m.description})
	}
	return out
}

// options are the session's config options, in codex-acp's order: mode,
// collaboration mode, model, and the model's efforts when it has any.
func (st *settings) options() []acp.SessionConfigOption {
	var modeOpts []acp.SessionConfigSelectOption
	for _, m := range modes {
		modeOpts = append(modeOpts, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueID(m.id), Name: m.name, Description: m.description})
	}
	out := []acp.SessionConfigOption{
		selectOption(optMode, "Mode", "Approval and sandboxing preset for the session", "mode", st.mode, modeOpts),
		selectOption(optCollab, "Collaboration mode", "How Codex collaborates for subsequent turns", "collaboration_mode", st.collab,
			[]acp.SessionConfigSelectOption{{Value: "default", Name: "Default"}, {Value: "plan", Name: "Plan", Description: "Plan before making changes"}}),
	}
	var models []acp.SessionConfigSelectOption
	if _, listed := st.current(); !listed {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueID(st.model), Name: displayName(st.model)})
	}
	for _, m := range st.models {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueID(m.ID), Name: displayName(m.DisplayName), Description: m.Description})
	}
	out = append(out, selectOption(optModel, "Model", "Model Codex uses for the session", "model", st.model, models))
	if m, ok := st.current(); ok && len(m.SupportedReasoningEfforts) > 0 {
		var efforts []acp.SessionConfigSelectOption
		for _, e := range m.SupportedReasoningEfforts {
			efforts = append(efforts, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueID(e.ReasoningEffort), Name: capitalize(e.ReasoningEffort), Description: e.Description})
		}
		out = append(out, selectOption(optEffort, "Reasoning effort", "How much reasoning effort the model should use", "thought_level", st.effort, efforts))
	}
	return out
}

func selectOption(id, name, description, category, current string, opts []acp.SessionConfigSelectOption) acp.SessionConfigOption {
	return acp.SessionConfigOption{ID: acp.SessionConfigID(id), Name: name, Description: description,
		Category: acp.SessionConfigOptionCategory(category), Type: "select",
		Select: &acp.SessionConfigSelect{CurrentValue: acp.SessionConfigValueID(current), Options: acp.SessionConfigSelectOptions{Ungrouped: opts}}}
}

// displayName is a model's name as codex-acp shows it: "gpt-5.5-codex"
// as "5.5 Codex", any other as it is.
func displayName(name string) string {
	if !strings.HasPrefix(strings.ToLower(name), "gpt-") {
		return name
	}
	words := strings.FieldsFunc(name[4:], func(r rune) bool { return r == '-' || r == '/' })
	for i, w := range words {
		words[i] = capitalize(w)
	}
	return strings.Join(words, " ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
