// Package types defines shared data types used across all layers.
package types

import "time"

// Role represents the sender role in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// ContentType identifies the type of a content block.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeImage      ContentType = "image"
	ContentTypeFile       ContentType = "file"
)

// ContentBlock is a single piece of content within a message.
type ContentBlock struct {
	Type       ContentType `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolUseID  string      `json:"tool_use_id,omitempty"`
	ToolName   string      `json:"tool_name,omitempty"`
	ToolInput  string      `json:"tool_input,omitempty"`  // JSON string
	ToolResult string      `json:"tool_result,omitempty"` // JSON string
	IsError    bool        `json:"is_error,omitempty"`
}

// Message represents a single message in a conversation.
type Message struct {
	ID        string         `json:"id"`
	Role      Role           `json:"role"`
	Content   []ContentBlock `json:"content"`
	CreatedAt time.Time      `json:"created_at"`
	Tokens    int            `json:"tokens,omitempty"`
}

// IncomingMessage is the standardized input from any channel.
type IncomingMessage struct {
	ChannelName string `json:"channel_name"` // e.g. "feishu", "websocket", "http"
	SessionID   string `json:"session_id"`
	UserID      string `json:"user_id"`
	Text        string `json:"text"`
	// Content holds multi-type content blocks (v1.5). When present, Text is
	// the concatenation of all text blocks for backward-compatible consumers.
	Content []IncomingContentBlock `json:"content,omitempty"`
	// ToolResult is set when the client returns a tool execution result (v1.1).
	ToolResult *ToolResultPayload `json:"tool_result,omitempty"`
	// PermissionResponse is set when the client approves/denies a permission request.
	PermissionResponse *PermissionResponse `json:"permission_response,omitempty"`
	// PlanResponse is set when the client approves/edits/rejects a plan
	// proposal (see PlanProposal). Routed to the engine's
	// SubmitPlanResponse method; the awaiting PlanCoordinator unblocks
	// and continues execution with the (possibly edited) plan.
	PlanResponse *PlanResponse `json:"plan_response,omitempty"`
	// StepDecisionResponse is the user's reply to a
	// step_decision_requested prompt (continue / retry / cancel after a
	// step or plan-level failure). Routed to QueryEngine.SubmitStepDecision.
	StepDecisionResponse *StepDecisionResponse `json:"step_decision_response,omitempty"`
	// RawPayload holds channel-specific original data.
	RawPayload map[string]any `json:"raw_payload,omitempty"`
	// CoordinatorMode pins the L2 coordinator mode for this turn (see
	// engine.CoordinatorMode). Allowed: "react" / "plan" / "" (auto).
	// Channel adapters copy from their wire-level field; the router
	// turns it into a tool.WithCoordinatorMode ctx value before
	// dispatching to the engine.
	CoordinatorMode string `json:"coordinator_mode,omitempty"`
	// PlanConfirmation controls whether plan-mode runs pause for user
	// review. Allowed: "" / "auto" (no pause, default), "required"
	// (every plan goes through plan.proposed → plan.response). Only
	// has effect when the resolved coordinator mode is plan.
	PlanConfirmation string `json:"plan_confirmation,omitempty"`
}

// IncomingContentBlock is a typed content block from a user message.
//
// For text blocks only Text is populated. For image and file blocks the source
// fields describe where to find the data (local path, remote URL, or inline
// base64).
type IncomingContentBlock struct {
	Type      string `json:"type"`                 // "text", "image", "file"
	Text      string `json:"text,omitempty"`       // for type=text
	MIMEType  string `json:"mime_type,omitempty"`  // e.g. "image/png", "text/csv"
	Path      string `json:"path,omitempty"`       // local filesystem path
	URL       string `json:"url,omitempty"`        // remote URL
	Data      string `json:"data,omitempty"`       // base64-encoded inline data
}

// ToolResultPayload carries the result of client-side tool execution.
type ToolResultPayload struct {
	ToolUseID    string `json:"tool_use_id"`
	Status       string `json:"status"`        // success, error, denied, timeout, cancelled
	Output       string `json:"output,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// Usage tracks token consumption for a single LLM call.
type Usage struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	CacheRead      int `json:"cache_read_tokens,omitempty"`
	CacheWrite     int `json:"cache_write_tokens,omitempty"`
	ThinkingTokens int `json:"thinking_tokens,omitempty"`
}
