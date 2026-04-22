// Package websocket implements a WebSocket channel that streams engine events
// to connected clients using a wire protocol (v1.1). Messages are JSON text
// frames with type/event_id/session_id fields. Type names use dot.notation.
package websocket

import (
	"bytes"
	"encoding/json"
	"fmt"

	"harnessclaw-go/pkg/types"
)

// WSMessageType identifies the kind of wire-protocol message.
type WSMessageType string

const (
	// Server → Client
	MsgTypeSessionCreated    WSMessageType = "session.created"
	MsgTypeSessionUpdated    WSMessageType = "session.updated"
	MsgTypeMessageStart      WSMessageType = "message.start"
	MsgTypeMessageDelta      WSMessageType = "message.delta"
	MsgTypeMessageStop       WSMessageType = "message.stop"
	MsgTypeContentStart      WSMessageType = "content.start"
	MsgTypeContentDelta      WSMessageType = "content.delta"
	MsgTypeContentStop       WSMessageType = "content.stop"
	MsgTypeToolCall          WSMessageType = "tool.call"          // client-side tool execution request
	MsgTypeToolStart         WSMessageType = "tool.start"         // server-side tool execution started
	MsgTypeToolEnd           WSMessageType = "tool.end"           // server-side tool execution completed
	MsgTypePermissionRequest WSMessageType = "permission.request" // server asks client for tool approval
	MsgTypeSubAgentStart     WSMessageType = "subagent.start"
	MsgTypeSubAgentEnd       WSMessageType = "subagent.end"
	MsgTypeSubAgentEvent     WSMessageType = "subagent.event"     // real-time sub-agent streaming
	// Phase 1.5
	MsgTypeAgentRouted      WSMessageType = "agent.routed"
	// Phase 2
	MsgTypeTaskCreated      WSMessageType = "task.created"
	MsgTypeTaskUpdated      WSMessageType = "task.updated"
	// Phase 3
	MsgTypeAgentMessage     WSMessageType = "agent.message"
	// Phase 4
	MsgTypeAgentSpawned     WSMessageType = "agent.spawned"
	MsgTypeAgentIdle        WSMessageType = "agent.idle"
	MsgTypeAgentCompleted   WSMessageType = "agent.completed"
	MsgTypeAgentFailed      WSMessageType = "agent.failed"
	// Phase 5
	MsgTypeTeamCreated      WSMessageType = "team.created"
	MsgTypeTeamMemberJoin   WSMessageType = "team.member_join"
	MsgTypeTeamMemberLeft   WSMessageType = "team.member_left"
	MsgTypeTeamDeleted      WSMessageType = "team.deleted"
	MsgTypeTaskEnd           WSMessageType = "task.end"
	MsgTypeError             WSMessageType = "error"
	MsgTypePong              WSMessageType = "pong"
)

// RenderHint classifies tool output for client-side rendering.
type RenderHint string

const (
	RenderTerminal RenderHint = "terminal"  // Bash: shell output
	RenderCode     RenderHint = "code"      // Read: source code with syntax highlighting
	RenderDiff     RenderHint = "diff"      // Edit: file diff/patch
	RenderFileInfo RenderHint = "file_info" // Write: file creation/overwrite confirmation
	RenderSearch   RenderHint = "search"    // Grep, Glob, WebSearch, TavilySearch
	RenderMarkdown RenderHint = "markdown"  // WebFetch: web content
	RenderAgent    RenderHint = "agent"     // Agent tool: sub-agent output
	RenderSkill    RenderHint = "skill"     // Skill invocation
	RenderTask     RenderHint = "task"      // Task management tools
	RenderMessage  RenderHint = "message"   // SendMessage
	RenderTeam     RenderHint = "team"      // TeamCreate/Delete
	RenderPlain    RenderHint = "plain"     // default fallback
)

// Well-known metadata keys promoted to top-level ToolEndMessage fields.
// Tools set these in ToolResult.Metadata; the mapper promotes them and
// removes them from the residual metadata map to avoid duplication.
const (
	MetaRenderHint = "render_hint"
	MetaLanguage   = "language"
	MetaFilePath   = "file_path"
)

const (
	// Client → Server
	MsgTypeSessionCreate      WSMessageType = "session.create" // client requests session initialization
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
	SubAgents   bool `json:"sub_agents"`
	Tasks       bool `json:"tasks"`
	Messaging   bool `json:"messaging"`
	AsyncAgent  bool `json:"async_agent"`
	Teams       bool `json:"teams"`
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
	ID    string     `json:"id"`
	Model string     `json:"model"`
	Role  string     `json:"role"`
	Usage *UsageInfo `json:"usage,omitempty"`
}

// MessageDeltaMessage carries end-of-message metadata (stop_reason, usage).
type MessageDeltaMessage struct {
	Type      WSMessageType    `json:"type"` // "message.delta"
	EventID   string           `json:"event_id"`
	SessionID string           `json:"session_id"`
	Delta     MessageDeltaInfo `json:"delta"`
	Usage     *UsageInfo       `json:"usage,omitempty"`
}

// MessageDeltaInfo carries the stop reason and optional error detail.
type MessageDeltaInfo struct {
	StopReason string       `json:"stop_reason"`
	Error      *ErrorDetail `json:"error,omitempty"`
}

// MessageStopMessage signals the end of an LLM response message.
type MessageStopMessage struct {
	Type      WSMessageType `json:"type"` // "message.stop"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
}

// ContentStartMessage signals the beginning of a content block.
type ContentStartMessage struct {
	Type         WSMessageType     `json:"type"` // "content.start"
	EventID      string            `json:"event_id"`
	SessionID    string            `json:"session_id"`
	Index        int               `json:"index"`
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
	Text        string `json:"text,omitempty"`         // for text_delta
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
	RenderHint RenderHint     `json:"render_hint,omitempty"`
	Language   string         `json:"language,omitempty"`
	FilePath   string         `json:"file_path,omitempty"`
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

// SubAgentStartMessage is sent when a sub-agent session begins.
type SubAgentStartMessage struct {
	Type          WSMessageType `json:"type"` // "subagent.start"
	EventID       string        `json:"event_id"`
	SessionID     string        `json:"session_id"`
	AgentID       string        `json:"agent_id"`
	AgentName     string        `json:"agent_name,omitempty"`
	Description   string        `json:"description,omitempty"`
	AgentType     string        `json:"agent_type"`
	ParentAgentID string        `json:"parent_agent_id,omitempty"`
}

// SubAgentEndMessage is sent when a sub-agent session completes.
type SubAgentEndMessage struct {
	Type        WSMessageType `json:"type"` // "subagent.end"
	EventID     string        `json:"event_id"`
	SessionID   string        `json:"session_id"`
	AgentID     string        `json:"agent_id"`
	AgentName   string        `json:"agent_name,omitempty"`
	Status      string        `json:"status"`
	DurationMs  int64         `json:"duration_ms"`
	NumTurns    int           `json:"num_turns,omitempty"`
	Usage       *UsageInfo    `json:"usage,omitempty"`
	DeniedTools []string      `json:"denied_tools,omitempty"`
}

// SubAgentEventMessage carries real-time streaming content from a sub-agent.
// It wraps the inner event so it doesn't interfere with the parent's message
// lifecycle in the mapper.
type SubAgentEventMessage struct {
	Type      WSMessageType              `json:"type"` // "subagent.event"
	EventID   string                     `json:"event_id"`
	SessionID string                     `json:"session_id"`
	AgentID   string                     `json:"agent_id"`
	AgentName string                     `json:"agent_name,omitempty"`
	Payload   *types.SubAgentEventData   `json:"payload"`
}

// AgentRoutedMessage notifies the client that a message was routed to a specialist agent.
type AgentRoutedMessage struct {
	Type        WSMessageType `json:"type"`
	EventID     string        `json:"event_id"`
	SessionID   string        `json:"session_id"`
	AgentName   string        `json:"agent_name"`
	DisplayName string        `json:"display_name,omitempty"`
	Description string        `json:"description,omitempty"`
	AutoTeam    bool          `json:"auto_team,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`
}

// TaskCreatedMessage notifies the client of a new task.
type TaskCreatedMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Task      TaskInfoWire  `json:"task"`
}

// TaskUpdatedMessage notifies the client of task changes.
type TaskUpdatedMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Task      TaskInfoWire  `json:"task"`
}

// TaskInfoWire is the wire format of task state.
type TaskInfoWire struct {
	TaskID     string `json:"task_id"`
	Subject    string `json:"subject"`
	Status     string `json:"status"`
	Owner      string `json:"owner,omitempty"`
	ActiveForm string `json:"active_form,omitempty"`
	ScopeID    string `json:"scope_id"`
}

// AgentMessageWireMessage notifies the client of inter-agent communication.
type AgentMessageWireMessage struct {
	Type      WSMessageType      `json:"type"`
	EventID   string             `json:"event_id"`
	SessionID string             `json:"session_id"`
	Message   AgentMsgInfoWire   `json:"message"`
}

// AgentMsgInfoWire is the wire format of agent message summary.
type AgentMsgInfoWire struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Summary string `json:"summary"`
	TeamID  string `json:"team_id,omitempty"`
}

// AgentSpawnedMessage notifies the client of a new async agent.
type AgentSpawnedMessage struct {
	Type          WSMessageType `json:"type"`
	EventID       string        `json:"event_id"`
	SessionID     string        `json:"session_id"`
	AgentID       string        `json:"agent_id"`
	AgentName     string        `json:"agent_name,omitempty"`
	Description   string        `json:"description,omitempty"`
	AgentType     string        `json:"agent_type"`
	ParentAgentID string        `json:"parent_agent_id,omitempty"`
}

// AgentIdleMessage notifies the client that an agent entered idle state.
type AgentIdleMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	AgentID   string        `json:"agent_id"`
	AgentName string        `json:"agent_name,omitempty"`
}

// AgentCompletedMessage notifies the client that an async agent completed.
type AgentCompletedMessage struct {
	Type       WSMessageType `json:"type"`
	EventID    string        `json:"event_id"`
	SessionID  string        `json:"session_id"`
	AgentID    string        `json:"agent_id"`
	AgentName  string        `json:"agent_name,omitempty"`
	Status     string        `json:"status"`
	DurationMs int64         `json:"duration_ms"`
	Usage      *UsageInfo    `json:"usage,omitempty"`
}

// AgentFailedMessage notifies the client that an async agent failed.
type AgentFailedMessage struct {
	Type       WSMessageType  `json:"type"`
	EventID    string         `json:"event_id"`
	SessionID  string         `json:"session_id"`
	AgentID    string         `json:"agent_id"`
	AgentName  string         `json:"agent_name,omitempty"`
	Error      ErrorDetail    `json:"error"`
	DurationMs int64          `json:"duration_ms"`
}

// TeamCreatedMessage notifies the client of a new team.
type TeamCreatedMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Team      TeamInfoWire  `json:"team"`
}

// TeamMemberJoinMessage notifies the client of a new team member.
type TeamMemberJoinMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Team      TeamInfoWire  `json:"team"`
}

// TeamMemberLeftMessage notifies the client of a departed team member.
type TeamMemberLeftMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Team      TeamInfoWire  `json:"team"`
}

// TeamDeletedMessage notifies the client of a dissolved team.
type TeamDeletedMessage struct {
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Team      TeamInfoWire  `json:"team"`
}

// TeamInfoWire is the wire format of team state.
type TeamInfoWire struct {
	TeamID     string   `json:"team_id"`
	TeamName   string   `json:"team_name"`
	Members    []string `json:"members,omitempty"`
	MemberName string   `json:"member_name,omitempty"`
	MemberType string   `json:"member_type,omitempty"`
}

// TaskEndMessage signals that a query-loop task has finished.
type TaskEndMessage struct {
	Type       WSMessageType `json:"type"` // "task.end"
	EventID    string        `json:"event_id"`
	SessionID  string        `json:"session_id"`
	RequestID  string        `json:"request_id,omitempty"`
	Status     string        `json:"status"`
	Message    string        `json:"message,omitempty"`
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
	Type      WSMessageType `json:"type"`
	EventID   string        `json:"event_id,omitempty"`
	SessionID string        `json:"session_id,omitempty"`

	// session.create fields
	UserID string `json:"user_id,omitempty"` // optional user identifier

	// user.message fields
	//
	// Content accepts either a single content block object or an array of
	// content blocks. Use ContentBlocks() to get the normalised slice.
	//   Single: {"type":"text","text":"hello"}
	//   Array:  [{"type":"text","text":"hello"},{"type":"image","source":{...}}]
	Content json.RawMessage `json:"content,omitempty"`
	Text    string          `json:"text,omitempty"` // shorthand: equivalent to [{"type":"text","text":"..."}]

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

// ContentBlocks parses the Content field into a normalised slice of content
// blocks. It handles three wire formats:
//  1. null / absent  → nil (caller should fall back to Text field)
//  2. single object  → one-element slice  (backward compat v1.4)
//  3. JSON array     → multi-element slice (v1.5)
func (m *ClientMessage) ContentBlocks() ([]ClientContentBlock, error) {
	if len(m.Content) == 0 {
		return nil, nil
	}
	// Peek at the first non-whitespace byte to distinguish object from array.
	trimmed := bytes.TrimLeft(m.Content, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var blocks []ClientContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			return nil, fmt.Errorf("invalid content array: %w", err)
		}
		return blocks, nil
	}
	// Single object — wrap in slice.
	var block ClientContentBlock
	if err := json.Unmarshal(m.Content, &block); err != nil {
		return nil, fmt.Errorf("invalid content object: %w", err)
	}
	return []ClientContentBlock{block}, nil
}

// ClientContentBlock is a single content block in a user.message.
//
// Wire formats by type:
//
//	text:  {"type":"text","text":"Hello"}
//	image: {"type":"image","source":{"type":"path","path":"/tmp/img.png"}}
//	       {"type":"image","source":{"type":"url","url":"https://..."}}
//	       {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//	file:  {"type":"file","source":{"type":"path","path":"/tmp/data.csv"}}
//	       {"type":"file","source":{"type":"url","url":"https://..."}}
type ClientContentBlock struct {
	Type   string               `json:"type"`             // "text", "image", "file"
	Text   string               `json:"text,omitempty"`   // for type=text
	Source *ClientContentSource `json:"source,omitempty"` // for type=image or type=file
}

// ClientContentSource describes the source of an image or file content block.
type ClientContentSource struct {
	Type      string `json:"type"`                 // "path", "url", "base64"
	Path      string `json:"path,omitempty"`       // for type=path: local filesystem path
	URL       string `json:"url,omitempty"`        // for type=url: remote URL
	Data      string `json:"data,omitempty"`       // for type=base64: base64-encoded data
	MediaType string `json:"media_type,omitempty"` // MIME type (e.g. "image/png"), required for base64
}

// ClientError is the error detail in a tool.result message.
type ClientError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
