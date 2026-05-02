package types

// ToolCall represents an LLM request to invoke a tool.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // raw JSON
}

// ToolResult is the output returned after tool execution.
type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error"`
	Metadata map[string]any `json:"metadata,omitempty"`

	// NewMessages are additional messages to inject into the conversation
	// AFTER the tool_result message. Used by SkillTool to inject the
	// expanded skill prompt as user messages (matching TS newMessages pattern).
	// These are transient — they go to the LLM but may not be persisted.
	NewMessages []Message `json:"new_messages,omitempty"`
}
