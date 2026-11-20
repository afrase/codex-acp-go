package appserver

import "encoding/json"

// Item is a thread item (ThreadItem): a message, a reasoning, a command,
// a file change, a tool call. Its fields are every kind's, flat, each set
// only for the kinds that have it; Raw is the item as sent.
type Item struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// userMessage's content is []UserInput; reasoning's []string
	// (Inputs, ReasoningText read it).
	Content json.RawMessage `json:"content"`
	Text    string          `json:"text"`    // agentMessage, plan
	Summary []string        `json:"summary"` // reasoning

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

	Query  string          `json:"query"`  // webSearch
	Action json.RawMessage `json:"action"` // webSearch: a SearchAction
	Path   string          `json:"path"`   // imageView
	Review string          `json:"review"` // enteredReviewMode, exitedReviewMode

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

// UnmarshalJSON keeps the item as sent in Raw.
func (it *Item) UnmarshalJSON(b []byte) error {
	type plain Item
	if err := json.Unmarshal(b, (*plain)(it)); err != nil {
		return err
	}
	it.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// Inputs is a userMessage's content.
func (it *Item) Inputs() []UserInput {
	var in []UserInput
	_ = json.Unmarshal(it.Content, &in)
	return in
}

// SearchAction is a web search's action, nil without one.
func (it *Item) SearchAction() *SearchAction {
	var a *SearchAction
	_ = json.Unmarshal(it.Action, &a)
	return a
}

// ReasoningText is a reasoning's raw content (its summary is Summary).
func (it *Item) ReasoningText() []string {
	var parts []string
	_ = json.Unmarshal(it.Content, &parts)
	return parts
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
