package codexacp

import (
	"context"
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"
)

// skillsList is skills/list's answer: each folder's skills.
type skillsList struct {
	Data []struct {
		Skills []struct {
			Name             string `json:"name"`
			Description      string `json:"description"`
			ShortDescription string `json:"shortDescription"`
		} `json:"skills"`
	} `json:"data"`
}

// commands sends the session's commands: Codex's skills of its folder as
// `$<name>`, which Codex reads in a prompt's text itself. Codex-acp's own
// commands (/plan, /compact, /review…) aren't offered yet. With
// onlyChanges, the same list isn't sent again.
func (s *session) commands(ctx context.Context, onlyChanges bool) {
	var list skillsList
	if err := s.a.call(ctx, "skills/list", map[string]any{"cwds": []string{s.cwd}}, &list); err != nil {
		s.a.log.Debug("skills not listed", "session", s.id, "err", err)
		return
	}
	var cmds []acp.AvailableCommand
	seen := map[string]bool{}
	for _, e := range list.Data {
		for _, sk := range e.Skills {
			name := "$" + sk.Name
			if seen[name] {
				continue
			}
			seen[name] = true
			desc := sk.ShortDescription
			if desc == "" {
				desc = sk.Description
			}
			if desc == "" {
				desc = sk.Name
			}
			cmds = append(cmds, acp.AvailableCommand{Name: name, Description: desc})
		}
	}
	if len(cmds) == 0 {
		return
	}
	key, _ := json.Marshal(cmds) //nolint:errchkjson // names and words
	s.mu.Lock()
	same := string(key) == s.published
	s.published = string(key)
	s.mu.Unlock()
	if onlyChanges && same {
		return
	}
	s.update(acp.SessionUpdate{SessionUpdate: "available_commands_update", AvailableCommandsUpdate: &acp.AvailableCommandsUpdate{AvailableCommands: cmds}})
}
