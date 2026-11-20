package appserver

import (
	"encoding/json"
	"fmt"
)

// Item is a thread item (ThreadItem): a message, a reasoning, a command,
// a file change, a tool call. Its fields are every kind's, flat, each set
// only for the kinds that have it; Raw is the item as sent.
//
// The messages that carry items (Turn, ItemNotification, ItemEntry) hold
// them as JSON, for DecodeItem to decode one at a time: an item that
// doesn't decode is then that item's failure, not its message's.
type Item struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// Content is userMessage's []UserInput (Inputs) and reasoning's
	// []string (ReasoningContent), as sent.
	Content json.RawMessage `json:"content"`
	Text    string          `json:"text"`    // agentMessage, plan
	Summary []string        `json:"summary"` // reasoning

	Inputs           []UserInput `json:"-"` // userMessage's content
	ReasoningContent []string    `json:"-"` // reasoning's raw content (its summary is Summary)

	// commandExecution
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	Source           string          `json:"source"`
	CommandActions   []CommandAction `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`

	// commandExecution, fileChange, mcpToolCall, dynamicToolCall,
	// collabAgentToolCall: inProgress, completed, failed, declined.
	Status string `json:"status"`

	Changes []FileChange `json:"changes"` // fileChange

	// mcpToolCall; Tool and Arguments dynamicToolCall's too.
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
	Namespace *string         `json:"namespace"`

	Query        string          `json:"query"`  // webSearch
	Action       json.RawMessage `json:"action"` // webSearch, as sent
	SearchAction *SearchAction   `json:"-"`      // webSearch's action, nil without one
	Path         string          `json:"path"`   // imageView
	Review       string          `json:"review"` // enteredReviewMode, exitedReviewMode

	// collabAgentToolCall
	Prompt            *string         `json:"prompt"`
	SenderThreadID    string          `json:"senderThreadId"`
	ReceiverThreadIDs []string        `json:"receiverThreadIds"`
	AgentsStates      json.RawMessage `json:"agentsStates"`
	Model             *string         `json:"model"`
	ReasoningEffort   *string         `json:"reasoningEffort"`

	// subAgentActivity
	Kind          string `json:"kind"`
	AgentThreadID string `json:"agentThreadId"`
	AgentPath     string `json:"agentPath"`

	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the item, its content and action by its kind
// too, and keeps it as sent in Raw.
func (it *Item) UnmarshalJSON(b []byte) error {
	type plain Item
	if err := json.Unmarshal(b, (*plain)(it)); err != nil {
		return err
	}
	var err error
	switch it.Type {
	case "userMessage":
		err = unmarshalSet(it.Content, &it.Inputs)
	case "reasoning":
		err = unmarshalSet(it.Content, &it.ReasoningContent)
	case "webSearch":
		err = unmarshalSet(it.Action, &it.SearchAction)
	}
	if err != nil {
		return err
	}
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// unmarshalSet decodes b into v when b is there.
func unmarshalSet(b json.RawMessage, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// DecodeItem decodes an item as a message carries it. When it doesn't
// decode (a field of a type this package doesn't expect), the error says
// why, and the Item has the Type and ID that could be read, for a log
// line or a lookup by id.
func DecodeItem(b json.RawMessage) (Item, error) {
	var it Item
	err := json.Unmarshal(b, &it)
	if err == nil {
		return it, nil
	}
	var id struct {
		Type json.RawMessage `json:"type"`
		ID   json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(b, &id)
	partial := Item{Raw: append(json.RawMessage(nil), b...)}
	_ = json.Unmarshal(id.Type, &partial.Type)
	_ = json.Unmarshal(id.ID, &partial.ID)
	return partial, fmt.Errorf("item %q: %w", partial.Type, err)
}

// CommandAction is what Codex makes of a command: a read, a file
// listing, a search, or unknown.
type CommandAction struct {
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Path    *string `json:"path"`
	Query   *string `json:"query"`
}

// FileChange is a file a fileChange changes.
type FileChange struct {
	Path string `json:"path"`
	Kind struct {
		Type     string  `json:"type"` // add, delete, update
		MovePath *string `json:"move_path"`
	} `json:"kind"`
	Diff string `json:"diff"`
}

// SearchAction is what a web search did.
type SearchAction struct {
	Type    string   `json:"type"` // search, openPage, findInPage, other
	Query   *string  `json:"query"`
	Queries []string `json:"queries"`
	URL     *string  `json:"url"`
	Pattern *string  `json:"pattern"`
}
