// Package websocket implements a WebSocket channel that streams engine events
// to connected clients using a wire protocol (v1.1). Messages are JSON text
// frames with type/event_id/session_id fields. Type names use dot.notation.
package websocket

// WSMessageType identifies the kind of wire-protocol message.
type WSMessageType string

const (
	// Server → Client
	MsgTypeSessionCreated WSMessageType = "session.created"
	MsgTypeSessionUpdated WSMessageType = "session.updated"
	MsgTypeMessageStart   WSMessageType = "message.start"
	MsgTypeMessageDelta   WSMessageType = "message.delta"
	MsgTypeMessageStop    WSMessageType = "message.stop"
	MsgTypeContentStart   WSMessageType = "content.start"
	MsgTypeContentDelta   WSMessageType = "content.delta"
	MsgTypeContentStop    WSMessageType = "content.stop"
	MsgTypeToolCall       WSMessageType = "tool.call"       // client-side tool execution request
	MsgTypeToolStart      WSMessageType = "tool.start"      // server-side tool execution started
	MsgTypeToolEnd        WSMessageType = "tool.end"        // server-side tool execution completed
	MsgTypePermissionRequest WSMessageType = "permission.request" // server asks client for tool approval
	MsgTypeTaskEnd        WSMessageType = "task.end"
	MsgTypeError          WSMessageType = "error"
	MsgTypePong           WSMessageType = "pong"

	// Client → Server
	MsgTypeSessionCreate      WSMessageType = "session.create"      // client requests session initialization
	MsgTypeUserMessage        WSMessageType = "user.message"
	MsgTypeToolResult         WSMessageType = "tool.result"
	MsgTypeToolProgress       WSMessageType = "tool.progress"
	MsgTypePermissionResponse WSMessageType = "permission.response" // client approves/denies a permission request
	MsgTypeSessionUpdate      WSMessageType = "session.update"
	MsgTypeSessionInterrupt   WSMessageType = "session.interrupt"
	MsgTypePing               WSMessageType = "ping"
)

// ---------------------------------------------------------------------------
// Server → Client messages
// ---------------------------------------------------------------------------

// SessionCreatedMessage is the first message sent after WebSocket upgrade.
type SessionCreatedMessage struct {
	Type            WSMessageType `json:"type"`
	EventID         string        `json:"event_id"`
	SessionID       string        `json:"session_id"`
	ProtocolVersion string        `json:"protocol_version"`
	Session         SessionInfo   `json:"session"`
}

// SessionInfo describes the session configuration and capabilities.
type SessionInfo struct {
	Model        string       `json:"model,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities declares what the server supports.
type Capabilities struct {
	Streaming   bool `json:"streaming"`
	Tools       bool `json:"tools"`
	ClientTools bool `json:"client_tools"`
	Thinking    bool `json:"thinking"`
	MultiTurn   bool `json:"multi_turn"`
	ImageInput  bool `json:"image_input"`
}

// MessageStartMessage signals the beginning of an LLM response message.
type MessageStartMessage struct {
	Type      WSMessageType    `json:"type"` // "message.start"
	EventID   string           `json:"event_id"`
	SessionID string           `json:"session_id"`
	RequestID string           `json:"request_id,omitempty"`
	Message   MessageStartInfo `json:"message"`
}

// MessageStartInfo carries metadata about the starting message.
type MessageStartInfo struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Role  string    `json:"role"`
	Usage *UsageInfo `json:"usage,omitempty"`
}

// MessageDeltaMessage carries end-of-message metadata (stop_reason, usage).
type MessageDeltaMessage struct {
	Type      WSMessageType     `json:"type"` // "message.delta"
	EventID   string            `json:"event_id"`
	SessionID string            `json:"session_id"`
	Delta     MessageDeltaInfo  `json:"delta"`
	Usage     *UsageInfo        `json:"usage,omitempty"`
}

// MessageDeltaInfo carries the stop reason.
type MessageDeltaInfo struct {
	StopReason string `json:"stop_reason"`
}

// MessageStopMessage signals the end of an LLM response message.
type MessageStopMessage struct {
	Type      WSMessageType `json:"type"` // "message.stop"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
}

// ContentStartMessage signals the beginning of a content block.
type ContentStartMessage struct {
	Type         WSMessageType    `json:"type"` // "content.start"
	EventID      string           `json:"event_id"`
	SessionID    string           `json:"session_id"`
	Index        int              `json:"index"`
	ContentBlock *ContentBlockInfo `json:"content_block,omitempty"`
}

// ContentDeltaMessage carries an incremental content update.
type ContentDeltaMessage struct {
	Type      WSMessageType `json:"type"` // "content.delta"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Index     int           `json:"index"`
	Delta     *Delta        `json:"delta,omitempty"`
}

// ContentStopMessage signals the end of a content block.
type ContentStopMessage struct {
	Type      WSMessageType `json:"type"` // "content.stop"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Index     int           `json:"index"`
}

// Delta carries incremental text or tool-input JSON.
type Delta struct {
	Type        string `json:"type"`                   // text_delta, input_json_delta
	Text        string `json:"text,omitempty"`          // for text_delta
	PartialJSON string `json:"partial_json,omitempty"` // for input_json_delta
}

// ContentBlockInfo describes a new content block (text, tool_use, thinking).
type ContentBlockInfo struct {
	Type string `json:"type"`           // "text", "tool_use", "thinking"
	ID   string `json:"id,omitempty"`   // tool call ID
	Name string `json:"name,omitempty"` // tool name
}

// ToolCallMessage is sent by the server to request client-side tool execution.
type ToolCallMessage struct {
	Type      WSMessageType          `json:"type"` // "tool.call"
	EventID   string                 `json:"event_id"`
	SessionID string                 `json:"session_id"`
	RequestID string                 `json:"request_id,omitempty"`
	ToolUseID string                 `json:"tool_use_id"`
	ToolName  string                 `json:"tool_name"`
	Input     map[string]interface{} `json:"input"`
}

// ToolStartMessage is sent when a server-side tool execution begins.
type ToolStartMessage struct {
	Type      WSMessageType          `json:"type"` // "tool.start"
	EventID   string                 `json:"event_id"`
	SessionID string                 `json:"session_id"`
	ToolUseID string                 `json:"tool_use_id"`
	ToolName  string                 `json:"tool_name"`
	Input     map[string]interface{} `json:"input"`
}

// ToolEndMessage is sent when a server-side tool execution completes.
type ToolEndMessage struct {
	Type       WSMessageType  `json:"type"` // "tool.end"
	EventID    string         `json:"event_id"`
	SessionID  string         `json:"session_id"`
	ToolUseID  string         `json:"tool_use_id"`
	ToolName   string         `json:"tool_name"`
	Status     string         `json:"status"` // "success" or "error"
	Output     string         `json:"output"`
	IsError    bool           `json:"is_error"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// PermissionRequestMessage is sent by the server when a tool needs user approval.
type PermissionRequestMessage struct {
	Type          WSMessageType          `json:"type"` // "permission.request"
	EventID       string                 `json:"event_id"`
	SessionID     string                 `json:"session_id"`
	RequestID     string                 `json:"request_id"`
	ToolName      string                 `json:"tool_name"`
	ToolInput     string                 `json:"tool_input"`
	Message       string                 `json:"message"`
	IsReadOnly    bool                   `json:"is_read_only"`
	Options       []PermissionOptionWire `json:"options"`
	PermissionKey string                 `json:"permission_key"` // session-allow granularity key (e.g. "Bash:git")
}

// PermissionOptionWire is the wire format of a permission choice.
type PermissionOptionWire struct {
	Label string `json:"label"`
	Scope string `json:"scope"` // "once" or "session"
	Allow bool   `json:"allow"`
}

// TaskEndMessage signals that a query-loop task has finished.
type TaskEndMessage struct {
	Type       WSMessageType `json:"type"` // "task.end"
	EventID    string        `json:"event_id"`
	SessionID  string        `json:"session_id"`
	RequestID  string        `json:"request_id,omitempty"`
	Status     string        `json:"status"`
	DurationMs int64         `json:"duration_ms"`
	NumTurns   int           `json:"num_turns"`
	TotalUsage *UsageInfo    `json:"total_usage,omitempty"`
}

// ErrorMessage delivers a structured error to the client.
type ErrorMessage struct {
	Type      WSMessageType `json:"type"` // "error"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	RequestID string        `json:"request_id,omitempty"`
	Error     ErrorDetail   `json:"error"`
}

// ErrorDetail is the structured error payload.
type ErrorDetail struct {
	Type         string `json:"type"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMs int    `json:"retry_after_ms,omitempty"`
}

// UsageInfo summarises token consumption.
type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_tokens,omitempty"`
	CacheWrite   int `json:"cache_write_tokens,omitempty"`
}

// AssistantMessage delivers a complete assistant turn (non-streaming fallback).
type AssistantMessage struct {
	Type      WSMessageType    `json:"type"`
	EventID   string           `json:"event_id"`
	SessionID string           `json:"session_id"`
	Message   AssistantContent `json:"message"`
}

// AssistantContent is the body of an AssistantMessage.
type AssistantContent struct {
	Role    string        `json:"role"`
	Content []ContentItem `json:"content"`
}

// ContentItem is one block inside an AssistantContent.
type ContentItem struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input string `json:"input,omitempty"`
}

// ---------------------------------------------------------------------------
// Client → Server messages
// ---------------------------------------------------------------------------

// ClientMessage is the generic envelope for messages sent by the client.
type ClientMessage struct {
	Type      WSMessageType          `json:"type"`
	EventID   string                 `json:"event_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`

	// session.create fields
	UserID string `json:"user_id,omitempty"` // optional user identifier

	// user.message fields
	Content *ClientContent `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"` // shorthand for content.text

	// tool.result fields
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Output    string                 `json:"output,omitempty"`
	Error     *ClientError           `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`

	// permission.response fields
	RequestID string `json:"request_id,omitempty"`
	Approved  *bool  `json:"approved,omitempty"` // pointer to distinguish unset from false
	Scope     string `json:"scope,omitempty"`    // "once" (default) or "session"
	Message   string `json:"message,omitempty"`  // reuse for denial reason
}

// ClientError is the error detail in a tool.result message.
type ClientError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClientContent is the content object in a user.message.
type ClientContent struct {
	Type string `json:"type"` // "text", "image", "file"
	Text string `json:"text,omitempty"`
}
