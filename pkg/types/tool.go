package types

// ToolCall represents an LLM request to invoke a tool.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // raw JSON
}

// ToolErrorType classifies a tool failure into the same controlled
// vocabulary the wire protocol (emit/v2 ErrorType) uses. Tools set this
// in ToolResult.ErrorType when returning IsError=true; the WebSocket
// translator passes it through as the card.close error.type so the
// front-end can pick the right icon / badge / user message without
// parsing free-text Content. Empty value defaults to "internal".
//
// We deliberately keep this as `string` (not a named enum type) so
// callers can write `types.ToolErrorInvalidInput` as a literal without
// importing the wire-protocol package — this file is the upstream of
// the dependency tree and emit/v2 imports types, not the other way
// around. The string values MUST match emit/v2.ErrorType values; a
// CI-time test in internal/emit/v2 keeps the two enums in sync.
type ToolErrorType = string

const (
	ToolErrorToolTimeout      ToolErrorType = "tool_timeout"
	ToolErrorRateLimit        ToolErrorType = "rate_limit"
	ToolErrorOverloaded       ToolErrorType = "overloaded"
	ToolErrorContractFail     ToolErrorType = "contract_fail"
	ToolErrorDependencyFail   ToolErrorType = "dependency_fail"
	ToolErrorPermissionDenied ToolErrorType = "permission_denied"
	ToolErrorInvalidInput     ToolErrorType = "invalid_input"
	ToolErrorUserAborted      ToolErrorType = "user_aborted"
	ToolErrorModelError       ToolErrorType = "model_error"
	ToolErrorInternal         ToolErrorType = "internal"
)

// ToolResult is the output returned after tool execution.
type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error"`
	Metadata map[string]any `json:"metadata,omitempty"`

	// ErrorType is the structured classification of a failure. Only
	// meaningful when IsError=true. Tools and the executor SHOULD set
	// this to one of the ToolError* constants so the wire translator
	// can emit the correct ErrorInfo.Type without string-parsing
	// Content. Empty (zero-value) on failure is treated as "internal".
	//
	// Why this field instead of post-hoc parsing in the translator:
	// classification at the source preserves semantic intent across
	// rewordings (changing "unknown tool: X" to "tool not registered: X"
	// would silently degrade a string classifier). The wire-protocol
	// side stays compile-time stable.
	ErrorType ToolErrorType `json:"error_type,omitempty"`

	// NewMessages are additional messages to inject into the conversation
	// AFTER the tool_result message. Used by SkillTool to inject the
	// expanded skill prompt as user messages (matching TS newMessages pattern).
	// These are transient — they go to the LLM but may not be persisted.
	NewMessages []Message `json:"new_messages,omitempty"`
}
