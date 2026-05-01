// Package websocket implements a WebSocket channel that streams engine events
// to connected clients using a wire protocol (v1.1). Messages are JSON text
// frames with type/event_id/session_id fields. Type names use dot.notation.
package websocket

import (
	"bytes"
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/emit"
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
	MsgTypeAgentIntent       WSMessageType = "agent.intent"       // model-supplied progress sentence before a tool runs
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
	// Deliverable
	MsgTypeDeliverableReady  WSMessageType = "deliverable.ready"
	MsgTypeError             WSMessageType = "error"
	MsgTypePong              WSMessageType = "pong"

	// --- Emit lifecycle events (protocol v1.11). The framework emits
	// these around the boundaries of a request, a plan, and each step in
	// the plan so observers can render the multi-agent execution tree
	// without having to parse the LLM transcript.
	//
	// NOTE: step.* (NOT task.*) is used for plan-step lifecycle. The
	// task.* namespace is reserved for the v1.7 user-facing TodoList
	// system (§6.8); reusing the prefix would make a one-level type
	// switch ambiguous on the client. ---
	MsgTypeTraceStarted   WSMessageType = "trace.started"
	MsgTypeTraceFinished  WSMessageType = "trace.finished"
	MsgTypeTraceFailed    WSMessageType = "trace.failed"
	MsgTypePlanCreated    WSMessageType = "plan.created"
	MsgTypePlanUpdated    WSMessageType = "plan.updated"
	MsgTypePlanCompleted  WSMessageType = "plan.completed"
	MsgTypePlanFailed     WSMessageType = "plan.failed"
	MsgTypeStepDispatched WSMessageType = "step.dispatched"
	MsgTypeStepStarted    WSMessageType = "step.started"
	MsgTypeStepProgress   WSMessageType = "step.progress"
	MsgTypeStepCompleted  WSMessageType = "step.completed"
	MsgTypeStepFailed     WSMessageType = "step.failed"
	MsgTypeStepSkipped    WSMessageType = "step.skipped"
	MsgTypeAgentHeartbeat WSMessageType = "agent.heartbeat"

	// Session continuity (v1.11+): client-driven event replay after a
	// reconnect. See §3.6 for the retention contract.
	MsgTypeSessionResumed      WSMessageType = "session.resumed"
	MsgTypeSessionResumeFailed WSMessageType = "session.resume_failed"
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
	MsgTypeSessionResume      WSMessageType = "session.resume" // client requests event replay after reconnect
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
	// Emit declares whether the server emits structured lifecycle events
	// (trace.*, plan.*, task.dispatched/completed/failed, agent.heartbeat)
	// with envelope/display/metrics envelopes. v1.11+.
	Emit bool `json:"emit"`
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
//
// Description is the short label dispatched alongside the task ("调研 LLM
// 推理"). Task carries the full prompt text the parent agent handed down,
// so the client can render "researcher 接到的任务：…" — without it, only
// the 3-5-word label reaches the user and the sub-agent's actual mission
// is invisible.
type SubAgentStartMessage struct {
	Type          WSMessageType `json:"type"` // "subagent.start"
	EventID       string        `json:"event_id"`
	SessionID     string        `json:"session_id"`
	AgentID       string        `json:"agent_id"`
	AgentName     string        `json:"agent_name,omitempty"`
	Description   string        `json:"description,omitempty"`
	Task          string        `json:"task,omitempty"`
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

// AgentIntentMessage carries a per-tool progress sentence the model
// provided via the framework-required `intent` field on every tool call.
// Emitted **before** tool.start so the user sees "researcher 正在搜索 vLLM
// 论文" the moment the call is dispatched.
//
// AgentID/AgentName identify which agent issued the call: empty for the
// main agent (emma); populated when the call originated inside a sub-agent
// (the SpawnSync forwarding loop wraps it as subagent.event{event_type=intent}
// instead — this top-level frame is for emma's own tool calls).
type AgentIntentMessage struct {
	Type      WSMessageType `json:"type"` // "agent.intent"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	AgentID   string        `json:"agent_id,omitempty"`
	AgentName string        `json:"agent_name,omitempty"`
	ToolUseID string        `json:"tool_use_id"`
	ToolName  string        `json:"tool_name"`
	Intent    string        `json:"intent"`
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

// DeliverableReadyMessage notifies the client that a sub-agent has produced
// a file deliverable. The client should render/download the file directly.
type DeliverableReadyMessage struct {
	Type      WSMessageType `json:"type"` // "deliverable.ready"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	AgentID   string        `json:"agent_id,omitempty"`
	AgentName string        `json:"agent_name,omitempty"`
	FilePath  string        `json:"file_path"`
	Language  string        `json:"language,omitempty"`
	ByteSize  int           `json:"byte_size,omitempty"`
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

// ---------------------------------------------------------------------------
// Emit envelope (v1.11+): common metadata attached to lifecycle events.
// ---------------------------------------------------------------------------

// EnvelopeWire is the on-the-wire representation of internal/emit.Envelope.
// All emit.* lifecycle events carry one. Consumers can route on envelope
// fields without parsing the per-type payload.
//
// The envelope is ADDITIVE — emit events still carry the message-level
// top-level fields (event_id, session_id) defined in §4 Message Format.
// The wire `event_id` IS the envelope event id; there is no duplication.
// `agent_id` lives only in the envelope for emit events; the legacy
// agent.* / subagent.* events keep their existing top-level agent_id.
type EnvelopeWire struct {
	TraceID       string `json:"trace_id"`
	ParentEventID string `json:"parent_event_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	ParentTaskID  string `json:"parent_task_id,omitempty"`
	Seq           int64  `json:"seq"`
	Timestamp     string `json:"timestamp"`
	AgentRole     string `json:"agent_role"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentRunID    string `json:"agent_run_id,omitempty"`
	Severity      string `json:"severity"`
}

// DisplayWire is the wire form of emit.Display — UI rendering hints.
type DisplayWire struct {
	Title       string `json:"title,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	PersonaHint string `json:"persona_hint,omitempty"`
}

// MetricsWire is the wire form of emit.Metrics — cost / perf telemetry.
type MetricsWire struct {
	DurationMs int64   `json:"duration_ms,omitempty"`
	TokensIn   int     `json:"tokens_in,omitempty"`
	TokensOut  int     `json:"tokens_out,omitempty"`
	CacheRead  int     `json:"cache_read_tokens,omitempty"`
	CacheWrite int     `json:"cache_write_tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Model      string  `json:"model,omitempty"`
}

// envelopeFromTypes converts the engine-side emit.Envelope into the wire
// envelope. Returns nil if env is nil. The Timestamp is rendered in
// RFC3339 with millisecond precision (UTC) so clients can parse it
// consistently.
func envelopeFromTypes(env *emit.Envelope) *EnvelopeWire {
	if env == nil {
		return nil
	}
	return &EnvelopeWire{
		TraceID:       env.TraceID,
		ParentEventID: env.ParentEventID,
		TaskID:        env.TaskID,
		ParentTaskID:  env.ParentTaskID,
		Seq:           env.Seq,
		Timestamp:     env.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
		AgentRole:     string(env.AgentRole),
		AgentID:       env.AgentID,
		AgentRunID:    env.AgentRunID,
		Severity:      string(env.Severity),
	}
}

func displayFromTypes(d *emit.Display) *DisplayWire {
	if d == nil {
		return nil
	}
	return &DisplayWire{
		Title:       d.Title,
		Summary:     d.Summary,
		Icon:        string(d.Icon),
		Visibility:  string(d.Visibility),
		PersonaHint: d.PersonaHint,
	}
}

func metricsFromTypes(m *emit.Metrics) *MetricsWire {
	if m == nil {
		return nil
	}
	return &MetricsWire{
		DurationMs: m.DurationMs,
		TokensIn:   m.TokensIn,
		TokensOut:  m.TokensOut,
		CacheRead:  m.CacheRead,
		CacheWrite: m.CacheWrite,
		CostUSD:    m.CostUSD,
		Model:      m.Model,
	}
}

// ---------------------------------------------------------------------------
// Emit lifecycle messages (v1.11+).
// ---------------------------------------------------------------------------

// TraceStartedMessage opens a new trace (one user-input → assistant-reply
// round). Every other event for that round carries the same trace_id in
// its envelope and is causally nested under this one.
type TraceStartedMessage struct {
	Type      WSMessageType `json:"type"` // "trace.started"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	RequestID string        `json:"request_id,omitempty"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Payload   TraceStartedPayload `json:"payload"`
}

// TraceStartedPayload describes what triggered the trace.
type TraceStartedPayload struct {
	UserInputSummary string `json:"user_input_summary,omitempty"`
	Channel          string `json:"channel,omitempty"`
}

// TraceFinishedMessage closes a trace successfully. Metrics carries the
// cumulative cost/duration for the whole request.
type TraceFinishedMessage struct {
	Type      WSMessageType `json:"type"` // "trace.finished"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	RequestID string        `json:"request_id,omitempty"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Metrics   *MetricsWire  `json:"metrics,omitempty"`
	Payload   TraceFinishedPayload `json:"payload"`
}

// TraceFinishedPayload summarises the trace outcome.
type TraceFinishedPayload struct {
	OutputSummary string `json:"output_summary,omitempty"`
	NumTurns      int    `json:"num_turns,omitempty"`
}

// TraceFailedMessage closes a trace with an error.
type TraceFailedMessage struct {
	Type      WSMessageType `json:"type"` // "trace.failed"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	RequestID string        `json:"request_id,omitempty"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Metrics   *MetricsWire  `json:"metrics,omitempty"`
	Payload   FailurePayload `json:"payload"`
}

// FailurePayload is the shared shape used by *.failed events. It splits
// the developer-facing detail (Type, Code, Message) from the user-facing
// fallback (UserMessage) so L1 can speak in persona.
//
// The shape is intentionally aligned with §6.12 ErrorDetail so that a
// single monitoring rule (`error.type == "tool_timeout"`) can match
// both connection-level errors and emit lifecycle failures.
type FailurePayload struct {
	Error    ErrorBody `json:"error"`
	Recovery *Recovery `json:"recovery,omitempty"`
}

// ErrorBody is the per-failure error block. Mirrors §6.12 ErrorDetail
// plus a UserMessage field for the persona-friendly fallback.
type ErrorBody struct {
	// Type is the controlled enum (see emit.ErrorType for values).
	// Required. Unknown values MUST be treated as "internal_error".
	Type string `json:"type"`
	// Code is a free-form machine-readable subtype scoped to the type
	// (e.g. type="tool_timeout" + code="BASH_TIMEOUT"). Optional.
	Code string `json:"code,omitempty"`
	// Message is the developer-facing description (may include stack
	// info, command, internal IDs).
	Message string `json:"message"`
	// UserMessage is the persona-friendly fallback. L1 SHOULD quote
	// this rather than the raw Message when relaying to the user.
	UserMessage string `json:"user_message,omitempty"`
	// Retryable signals whether automatic retry is sensible.
	Retryable bool `json:"retryable,omitempty"`
}

// Recovery describes what the framework chose to do about a failure.
type Recovery struct {
	Action     string `json:"action"`     // "retry" | "fallback" | "abort"
	NextTaskID string `json:"next_task_id,omitempty"`
}

// PlanCreatedMessage carries the validated plan once the L2 layer has
// produced and accepted it.
type PlanCreatedMessage struct {
	Type      WSMessageType `json:"type"` // "plan.created"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Payload   PlanPayload   `json:"payload"`
}

// PlanUpdatedMessage carries a re-planned graph (e.g. after a step
// failure forced the planner to revise).
type PlanUpdatedMessage struct {
	Type      WSMessageType `json:"type"` // "plan.updated"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Payload   PlanPayload   `json:"payload"`
}

// PlanCompletedMessage signals the plan finished (all steps terminal).
type PlanCompletedMessage struct {
	Type      WSMessageType `json:"type"` // "plan.completed"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Display   *DisplayWire  `json:"display,omitempty"`
	Metrics   *MetricsWire  `json:"metrics,omitempty"`
	Payload   PlanPayload   `json:"payload"`
}

// PlanFailedMessage signals the plan itself failed (e.g. PlanAgent could
// not produce a valid plan after MaxPlannerAttempts, or every step in
// the plan failed). Distinguished from plan.completed so the client can
// surface a different state.
type PlanFailedMessage struct {
	Type      WSMessageType  `json:"type"` // "plan.failed"
	EventID   string         `json:"event_id"`
	SessionID string         `json:"session_id"`
	Envelope  *EnvelopeWire  `json:"envelope"`
	Display   *DisplayWire   `json:"display,omitempty"`
	Metrics   *MetricsWire   `json:"metrics,omitempty"`
	Payload   FailurePayload `json:"payload"`
}

// PlanPayload is the shared body for plan.* events.
type PlanPayload struct {
	PlanID   string             `json:"plan_id"`
	Goal     string             `json:"goal,omitempty"`
	Strategy string             `json:"strategy,omitempty"`
	Status   string             `json:"status,omitempty"` // "created" | "updated" | "completed" | "failed"
	Tasks    []PlanTaskInfoWire `json:"tasks,omitempty"`
}

// PlanTaskInfoWire mirrors types.PlanTaskInfo for the wire.
type PlanTaskInfoWire struct {
	TaskID            string   `json:"task_id"`
	SubagentType      string   `json:"subagent_type"`
	DependsOn         []string `json:"depends_on,omitempty"`
	UserFacingTitle   string   `json:"user_facing_title,omitempty"`
	UserFacingSummary string   `json:"user_facing_summary,omitempty"`
}

// StepDispatchedMessage signals that L2 dispatched a sub-agent for one
// step of the active plan.
type StepDispatchedMessage struct {
	Type      WSMessageType         `json:"type"` // "step.dispatched"
	EventID   string                `json:"event_id"`
	SessionID string                `json:"session_id"`
	Envelope  *EnvelopeWire         `json:"envelope"`
	Display   *DisplayWire          `json:"display,omitempty"`
	Payload   StepDispatchedPayload `json:"payload"`
}

// StepDispatchedPayload describes the dispatch. step_id is the L2-step
// identifier (== envelope.task_id); subagent_type is which worker class
// will pick it up.
type StepDispatchedPayload struct {
	StepID       string `json:"step_id"`
	SubagentType string `json:"subagent_type,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	InputSummary string `json:"input_summary,omitempty"`
}

// StepStartedMessage signals that the worker assigned to a dispatched
// step has actually begun execution. Lets the client distinguish "queued"
// from "running" — task.dispatched alone may sit in the wave queue for
// some time when MaxParallel is set.
type StepStartedMessage struct {
	Type      WSMessageType      `json:"type"` // "step.started"
	EventID   string             `json:"event_id"`
	SessionID string             `json:"session_id"`
	Envelope  *EnvelopeWire      `json:"envelope"`
	Display   *DisplayWire       `json:"display,omitempty"`
	Payload   StepStartedPayload `json:"payload"`
}

// StepStartedPayload identifies the step + worker that just started.
type StepStartedPayload struct {
	StepID  string `json:"step_id"`
	AgentID string `json:"agent_id,omitempty"`
}

// StepCompletedMessage closes a dispatched step with success.
type StepCompletedMessage struct {
	Type      WSMessageType        `json:"type"` // "step.completed"
	EventID   string               `json:"event_id"`
	SessionID string               `json:"session_id"`
	Envelope  *EnvelopeWire        `json:"envelope"`
	Display   *DisplayWire         `json:"display,omitempty"`
	Metrics   *MetricsWire         `json:"metrics,omitempty"`
	Payload   StepCompletedPayload `json:"payload"`
}

// StepCompletedPayload describes the completion.
type StepCompletedPayload struct {
	StepID        string   `json:"step_id"`
	OutputSummary string   `json:"output_summary,omitempty"`
	Attempts      int      `json:"attempts,omitempty"`
	Deliverables  []string `json:"deliverables,omitempty"`
}

// StepFailedMessage closes a dispatched step with failure.
type StepFailedMessage struct {
	Type      WSMessageType  `json:"type"` // "step.failed"
	EventID   string         `json:"event_id"`
	SessionID string         `json:"session_id"`
	Envelope  *EnvelopeWire  `json:"envelope"`
	Display   *DisplayWire   `json:"display,omitempty"`
	Metrics   *MetricsWire   `json:"metrics,omitempty"`
	Payload   FailurePayload `json:"payload"`
}

// StepSkippedMessage marks a step as skipped — typically because an
// upstream dependency failed. Skipped steps never run.
type StepSkippedMessage struct {
	Type      WSMessageType      `json:"type"` // "step.skipped"
	EventID   string             `json:"event_id"`
	SessionID string             `json:"session_id"`
	Envelope  *EnvelopeWire      `json:"envelope"`
	Display   *DisplayWire       `json:"display,omitempty"`
	Payload   StepSkippedPayload `json:"payload"`
}

// StepSkippedPayload describes why the step was skipped.
type StepSkippedPayload struct {
	StepID string `json:"step_id"`
	Reason string `json:"reason,omitempty"`
}

// StepProgressMessage reports incremental progress for a long-running
// step. Producers MUST throttle these (≥ 200ms between events) to avoid
// flooding the client.
type StepProgressMessage struct {
	Type      WSMessageType       `json:"type"` // "step.progress"
	EventID   string              `json:"event_id"`
	SessionID string              `json:"session_id"`
	Envelope  *EnvelopeWire       `json:"envelope"`
	Display   *DisplayWire        `json:"display,omitempty"`
	Payload   StepProgressPayload `json:"payload"`
}

// StepProgressPayload describes the progress tick.
type StepProgressPayload struct {
	StepID         string  `json:"step_id"`
	ProgressPct    float64 `json:"progress_pct,omitempty"` // 0.0 — 1.0
	Stage          string  `json:"stage,omitempty"`
	ItemsProcessed int     `json:"items_processed,omitempty"`
}

// AgentHeartbeatMessage proves a long-running agent is still alive.
// Clients use this to detect stuck agents (started but no finished/failed).
type AgentHeartbeatMessage struct {
	Type      WSMessageType `json:"type"` // "agent.heartbeat"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	Envelope  *EnvelopeWire `json:"envelope"`
	Payload   AgentHeartbeatPayload `json:"payload"`
}

// AgentHeartbeatPayload identifies which agent emitted the heartbeat.
type AgentHeartbeatPayload struct {
	AgentID    string `json:"agent_id"`
	Stage      string `json:"stage,omitempty"`
	UptimeMs   int64  `json:"uptime_ms,omitempty"`
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

	// session.resume fields (v1.11+). The client supplies the trace it
	// was watching and the last seq it actually received; the server
	// responds with session.resumed (events replayed) or
	// session.resume_failed (events expired / unknown trace).
	TraceID string `json:"trace_id,omitempty"`
	LastSeq int64  `json:"last_seq,omitempty"`
}

// SessionResumedMessage acknowledges a successful resume. After this
// message the server replays buffered events whose seq > last_seq, in
// the original order, before any new events.
type SessionResumedMessage struct {
	Type      WSMessageType `json:"type"` // "session.resumed"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	TraceID   string        `json:"trace_id"`
	FromSeq   int64         `json:"from_seq"` // first replayed seq (inclusive)
	ToSeq     int64         `json:"to_seq"`   // last replayed seq (inclusive)
}

// SessionResumeFailedMessage signals that resume could not proceed —
// usually because the requested trace is older than the retention
// window. The client should fall back to a full refresh (reload state
// from REST history if available, or simply discard the in-memory
// representation of that trace).
type SessionResumeFailedMessage struct {
	Type      WSMessageType `json:"type"` // "session.resume_failed"
	EventID   string        `json:"event_id"`
	SessionID string        `json:"session_id"`
	TraceID   string        `json:"trace_id,omitempty"`
	Reason    string        `json:"reason"` // "events_expired" | "unknown_trace" | "session_not_found"
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
