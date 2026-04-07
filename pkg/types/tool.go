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
}
