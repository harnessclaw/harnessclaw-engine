package types

import "encoding/json"

// ExecutionContext provides the full context for a tool execution within the query loop.
// This mirrors the TypeScript ToolUseContext — the engine populates it before each tool call.
type ExecutionContext struct {
	// SessionID is the active conversation session.
	SessionID string `json:"session_id"`

	// UserID is the authenticated user.
	UserID string `json:"user_id"`

	// ChannelName identifies the originating channel (feishu, websocket, http).
	ChannelName string `json:"channel_name"`

	// Messages is the current conversation history (read-only snapshot).
	Messages []Message `json:"messages"`

	// ToolCallID is the unique ID from the LLM's tool_use block.
	ToolCallID string `json:"tool_call_id"`

	// ToolName is the tool being invoked.
	ToolName string `json:"tool_name"`

	// ToolInput is the raw JSON input from the LLM.
	ToolInput json.RawMessage `json:"tool_input"`

	// AbortFunc can be called to signal an early abort of the query loop.
	AbortFunc func() `json:"-"`
}
