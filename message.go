package compression

// Role identifies the role of a message in the normalized conversation model.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall describes one tool invocation emitted by an assistant message.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is the normalized input and output representation used by the
// middleware. Provider-specific message conversion is intentionally outside
// this package.
type Message struct {
	ID          string `json:"id"`
	Role        Role   `json:"role"`
	Content     string `json:"content"`
	ContentType string `json:"content_type,omitempty"`

	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`

	// Protected and Tags are caller-provided compression hints. They do not
	// change the protocol role or privilege of a message.
	Protected bool     `json:"protected,omitempty"`
	Tags      []string `json:"tags,omitempty"`

	// Synthetic and SourceIDs identify middleware-generated summary messages.
	Synthetic bool     `json:"synthetic,omitempty"`
	SourceIDs []string `json:"source_ids,omitempty"`
}

// Request contains a normalized conversation and the available model budget.
type Request struct {
	Messages      []Message
	ContextLimit  int
	OutputReserve int
	SafetyMargin  int
}

// FitStatus describes whether the returned messages can be sent to the model.
type FitStatus string

const (
	Fit          FitStatus = "fit"
	Degraded     FitStatus = "degraded"
	CannotFit    FitStatus = "cannot_fit"
	InvalidInput FitStatus = "invalid_input"
)

// Action records one compression operation.
type Action struct {
	UnitID       string
	Type         string
	Reason       string
	BeforeTokens int
	AfterTokens  int
}

// Diagnostic records an input, compression, or validation event.
type Diagnostic struct {
	Level     string
	Code      string
	Message   string
	MessageID string
	UnitID    string
}

// Result is returned by Middleware.Compress.
type Result struct {
	Messages      []Message
	Status        FitStatus
	BeforeTokens  int
	AfterTokens   int
	Actions       []Action
	Diagnostics   []Diagnostic
	FailureReason string
}
