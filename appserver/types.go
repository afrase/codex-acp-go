package appserver

import "encoding/json"

// The messages of the app-server's protocol (v2, with experimentalApi)
// that the agent uses, by hand from the schema `codex app-server
// generate-ts --experimental` prints (Codex 0.163.0; openai/codex,
// Apache-2.0): only the fields read or sent. Methods are named where
// they're used.

// InitializeParams is initialize's.
type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities"`
}

// ClientInfo names the client to Codex (its logs, its originator).
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// InitializeCapabilities is what the client opts into.
type InitializeCapabilities struct {
	ExperimentalAPI    bool `json:"experimentalApi"`
	RequestAttestation bool `json:"requestAttestation"`
}

// AccountReadParams is account/read's.
type AccountReadParams struct {
	RefreshToken bool `json:"refreshToken"`
}

// AccountReadResponse says whether Codex has a login, and needs one.
type AccountReadResponse struct {
	Account            json.RawMessage `json:"account"` // null without a login
	RequiresOpenaiAuth bool            `json:"requiresOpenaiAuth"`
}

// SignedIn says Codex can run a turn: it has an account, or its provider
// needs none.
func (r AccountReadResponse) SignedIn() bool {
	return !r.RequiresOpenaiAuth || (len(r.Account) > 0 && string(r.Account) != "null")
}

// ThreadStartParams is thread/start's.
type ThreadStartParams struct {
	Cwd    string         `json:"cwd,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

// ThreadResumeParams is thread/resume's.
type ThreadResumeParams struct {
	ThreadID     string         `json:"threadId"`
	Cwd          string         `json:"cwd,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	ExcludeTurns bool           `json:"excludeTurns,omitempty"`
}

// ThreadForkParams is thread/fork's.
type ThreadForkParams struct {
	ThreadID     string         `json:"threadId"`
	LastTurnID   string         `json:"lastTurnId,omitempty"`
	Cwd          string         `json:"cwd,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	ExcludeTurns bool           `json:"excludeTurns,omitempty"`
}

// ThreadOpened is what thread/start, thread/resume and thread/fork answer.
type ThreadOpened struct {
	Thread          Thread  `json:"thread"`
	Model           string  `json:"model"`
	ModelProvider   string  `json:"modelProvider"`
	ServiceTier     *string `json:"serviceTier"`
	ReasoningEffort *string `json:"reasoningEffort"`
	// CollaborationMode is thread/resume's only.
	CollaborationMode *CollaborationMode `json:"collaborationMode"`
	// ItemsBackwardsCursor is thread/resume's: where a paginated
	// thread's items are listed back from.
	ItemsBackwardsCursor *string `json:"itemsBackwardsCursor"`
}

// Thread is a Codex thread, an ACP session.
type Thread struct {
	ID              string  `json:"id"`
	Preview         string  `json:"preview"`
	Name            *string `json:"name"`
	Cwd             string  `json:"cwd"`
	UpdatedAt       int64   `json:"updatedAt"`   // Unix seconds
	HistoryMode     string  `json:"historyMode"` // "legacy" or "paginated"
	Model           *string `json:"model"`
	ModelProvider   string  `json:"modelProvider"`
	ReasoningEffort *string `json:"reasoningEffort"`
	Turns           []Turn  `json:"turns"`
}

// ThreadID names a thread, the params of the calls that need nothing more
// (thread/archive, thread/unsubscribe).
type ThreadID struct {
	ThreadID string `json:"threadId"`
}

// ThreadReadParams is thread/read's.
type ThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns,omitempty"`
}

// ThreadReadResponse is thread/read's answer.
type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
}

// ThreadListParams is thread/list's.
type ThreadListParams struct {
	Cursor         *string  `json:"cursor,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	SourceKinds    []string `json:"sourceKinds,omitempty"`
	UseStateDBOnly bool     `json:"useStateDbOnly"`
}

// ThreadListResponse is a page of threads.
type ThreadListResponse struct {
	Data       []Thread `json:"data"`
	NextCursor *string  `json:"nextCursor"`
}

// ThreadItemsListParams is thread/items/list's.
type ThreadItemsListParams struct {
	ThreadID      string  `json:"threadId"`
	Cursor        *string `json:"cursor"`
	Limit         int     `json:"limit,omitempty"`
	SortDirection string  `json:"sortDirection,omitempty"` // asc, desc
}

// ThreadItemsListResponse is a page of a thread's items.
type ThreadItemsListResponse struct {
	Data            []ItemEntry `json:"data"`
	NextCursor      *string     `json:"nextCursor"`
	BackwardsCursor *string     `json:"backwardsCursor"`
}

// ItemEntry is an item (for DecodeItem) and the turn it is in.
type ItemEntry struct {
	TurnID string          `json:"turnId"`
	Item   json.RawMessage `json:"item"`
}

// ModelListParams is model/list's.
type ModelListParams struct {
	Cursor *string `json:"cursor,omitempty"`
}

// ModelListResponse is a page of models.
type ModelListResponse struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// Model is a model Codex offers.
type Model struct {
	ID                        string         `json:"id"`
	DisplayName               string         `json:"displayName"`
	Description               string         `json:"description"`
	Hidden                    bool           `json:"hidden"`
	SupportedReasoningEfforts []EffortOption `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string         `json:"defaultReasoningEffort"`
	InputModalities           []string       `json:"inputModalities"`
	IsDefault                 bool           `json:"isDefault"`
}

// EffortOption is a reasoning effort a model takes.
type EffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

// CollaborationMode is Codex's collaboration mode ("default" or "plan").
type CollaborationMode struct {
	Mode     string                    `json:"mode"`
	Settings CollaborationModeSettings `json:"settings"`
}

// CollaborationModeSettings is a collaboration mode's model and effort.
type CollaborationModeSettings struct {
	Model                 string  `json:"model"`
	ReasoningEffort       *string `json:"reasoning_effort"`
	DeveloperInstructions *string `json:"developer_instructions"`
}

// TurnStartParams is turn/start's.
type TurnStartParams struct {
	ThreadID          string         `json:"threadId"`
	Input             []UserInput    `json:"input"`
	ApprovalPolicy    string         `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer string         `json:"approvalsReviewer,omitempty"`
	SandboxPolicy     map[string]any `json:"sandboxPolicy,omitempty"`
	Model             string         `json:"model,omitempty"`
	Effort            string         `json:"effort,omitempty"`
	Summary           string         `json:"summary,omitempty"`
}

// TurnStartResponse is turn/start's answer.
type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

// TurnInterruptParams is turn/interrupt's.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// TurnSteerParams is turn/steer's: input added to the turn running.
type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []UserInput `json:"input"`
}

// UserInput is a piece of a prompt: text or an image (a URL, often a
// data: URL).
type UserInput struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	TextElements []any  `json:"text_elements,omitempty"`
	URL          string `json:"url,omitempty"`
	Path         string `json:"path,omitempty"` // localImage's, mention's, skill's
	Name         string `json:"name,omitempty"` // mention's, skill's
}

// MarshalJSON writes text_elements for text, which Codex requires.
func (u UserInput) MarshalJSON() ([]byte, error) {
	type plain UserInput
	if u.Type == "text" {
		return json.Marshal(struct {
			plain
			TextElements []any `json:"text_elements"`
		}{plain(u), append([]any{}, u.TextElements...)})
	}
	return json.Marshal(plain(u))
}

// Turn is a turn of a thread.
type Turn struct {
	ID     string            `json:"id"`
	Items  []json.RawMessage `json:"items"`  // each for DecodeItem
	Status string            `json:"status"` // completed, interrupted, failed, inProgress
	Error  *TurnError        `json:"error"`
}

// TurnError is why a turn failed.
type TurnError struct {
	Message        string          `json:"message"`
	CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
}

// ErrorInfo is CodexErrorInfo when it's a plain string ("unauthorized",
// "usageLimitExceeded"), else "".
func (e *TurnError) ErrorInfo() string {
	if e == nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(e.CodexErrorInfo, &s)
	return s
}

// TurnNotification is turn/started's and turn/completed's.
type TurnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

// ItemNotification is item/started's and item/completed's.
type ItemNotification struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"` // for DecodeItem
}

// Delta is a piece of an item's text or output: item/agentMessage/delta,
// item/reasoning/*Delta, item/commandExecution/outputDelta,
// item/mcpToolCall/progress (Message), terminalInteraction (Stdin).
type Delta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
	Message  string `json:"message"`
	Stdin    string `json:"stdin"`
}

// TokenUsageNotification is thread/tokenUsage/updated's.
type TokenUsageNotification struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	TokenUsage struct {
		Total              TokenCount `json:"total"`
		Last               TokenCount `json:"last"`
		ModelContextWindow *int64     `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

// TokenCount is a breakdown of tokens.
type TokenCount struct {
	TotalTokens           int64 `json:"totalTokens"`
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
}

// ThreadNameNotification is thread/name/updated's.
type ThreadNameNotification struct {
	ThreadID   string  `json:"threadId"`
	ThreadName *string `json:"threadName"`
}

// PlanNotification is turn/plan/updated's.
type PlanNotification struct {
	ThreadID string `json:"threadId"`
	Plan     []struct {
		Step   string `json:"step"`
		Status string `json:"status"` // pending, inProgress, completed
	} `json:"plan"`
}

// ErrorNotification is error's: a turn's error, which Codex may retry.
type ErrorNotification struct {
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	WillRetry bool      `json:"willRetry"`
	Error     TurnError `json:"error"`
}

// WarningNotification is warning's, configWarning's (Summary, Details)
// and deprecationNotice's.
type WarningNotification struct {
	ThreadID *string `json:"threadId"`
	Message  string  `json:"message"`
	Summary  string  `json:"summary"`
	Details  *string `json:"details"`
}

// CommandApproval is item/commandExecution/requestApproval's params.
type CommandApproval struct {
	ThreadID                        string            `json:"threadId"`
	TurnID                          string            `json:"turnId"`
	ItemID                          string            `json:"itemId"`
	Reason                          *string           `json:"reason"`
	Command                         *string           `json:"command"`
	Cwd                             *string           `json:"cwd"`
	CommandActions                  []CommandAction   `json:"commandActions"`
	NetworkApprovalContext          *NetworkContext   `json:"networkApprovalContext"`
	AdditionalPermissions           json.RawMessage   `json:"additionalPermissions"`
	ProposedExecpolicyAmendment     []string          `json:"proposedExecpolicyAmendment"`
	ProposedNetworkPolicyAmendments []json.RawMessage `json:"proposedNetworkPolicyAmendments"`
	AvailableDecisions              []json.RawMessage `json:"availableDecisions"`
}

// NetworkContext is the host a command asks to reach.
type NetworkContext struct {
	Host     string `json:"host"`
	Protocol string `json:"protocol"`
}

// FileChangeApproval is item/fileChange/requestApproval's params.
type FileChangeApproval struct {
	ThreadID string  `json:"threadId"`
	TurnID   string  `json:"turnId"`
	ItemID   string  `json:"itemId"`
	Reason   *string `json:"reason"`
}

// Decision is an approval's answer: a decision, a string or an object.
type Decision struct {
	Decision any `json:"decision"`
}
