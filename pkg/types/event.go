package types

// StreamEventType classifies events emitted by an LLM provider stream.
type StreamEventType string

const (
	StreamEventText       StreamEventType = "text"
	StreamEventToolUse    StreamEventType = "tool_use"
	StreamEventMessageEnd StreamEventType = "message_end"
	StreamEventError      StreamEventType = "error"
)

// StreamEvent is a single event from a streaming LLM response.
type StreamEvent struct {
	Type       StreamEventType `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Error      error           `json:"-"`
}

// EngineEventType classifies events emitted by the query engine.
type EngineEventType string

const (
	EngineEventText              EngineEventType = "text"
	EngineEventToolUse           EngineEventType = "tool_use"            // LLM requests tool use (content block)
	EngineEventToolStart         EngineEventType = "tool_start"          // server-side tool execution begins
	EngineEventToolEnd           EngineEventType = "tool_end"            // server-side tool execution completes
	EngineEventToolCall          EngineEventType = "tool_call"           // server→client: request client-side tool execution
	EngineEventPermissionRequest EngineEventType = "permission_request"  // server→client: request permission approval
	EngineEventError             EngineEventType = "error"
	EngineEventDone              EngineEventType = "done"
	EngineEventMessageStart      EngineEventType = "message_start"      // LLM call begins streaming
	EngineEventMessageDelta      EngineEventType = "message_delta"      // LLM call metadata (stop_reason, usage)
	EngineEventMessageStop       EngineEventType = "message_stop"       // LLM call streaming ended
	EngineEventSubAgentStart     EngineEventType = "subagent_start"     // sub-agent session begins
	EngineEventSubAgentEnd       EngineEventType = "subagent_end"       // sub-agent session completes
	EngineEventSubAgentEvent     EngineEventType = "subagent_event"     // real-time sub-agent streaming event

	// Phase 1.5: @-mention routing
	EngineEventAgentRouted     EngineEventType = "agent_routed"      // @-mention routed to agent

	// Phase 2: Tasks
	EngineEventTaskCreated     EngineEventType = "task_created"      // task created
	EngineEventTaskUpdated     EngineEventType = "task_updated"      // task status/property changed

	// Phase 3: Messaging
	EngineEventAgentMessage    EngineEventType = "agent_message"     // inter-agent message

	// Phase 4: Async agents
	EngineEventAgentSpawned    EngineEventType = "agent_spawned"     // async agent launched
	EngineEventAgentIdle       EngineEventType = "agent_idle"        // agent entered idle
	EngineEventAgentCompleted  EngineEventType = "agent_completed"   // async agent done
	EngineEventAgentFailed     EngineEventType = "agent_failed"      // async agent failed

	// Phase 5: Teams
	EngineEventTeamCreated     EngineEventType = "team_created"      // team created
	EngineEventTeamMemberJoin  EngineEventType = "team_member_join"  // member joined
	EngineEventTeamMemberLeft  EngineEventType = "team_member_left"  // member left
	EngineEventTeamDeleted     EngineEventType = "team_deleted"      // team dissolved
)

// EngineEvent is a single event emitted from the engine to a channel.
type EngineEvent struct {
	Type              EngineEventType    `json:"type"`
	Text              string             `json:"text,omitempty"`
	ToolName          string             `json:"tool_name,omitempty"`
	ToolInput         string             `json:"tool_input,omitempty"`
	ToolUseID         string             `json:"tool_use_id,omitempty"`  // for tool_call events
	ToolResult        *ToolResult        `json:"tool_result,omitempty"`
	PermissionRequest *PermissionRequest `json:"permission_request,omitempty"` // for permission_request events
	Error             error              `json:"-"`
	Usage             *Usage             `json:"usage,omitempty"`
	Terminal          *Terminal          `json:"terminal,omitempty"`     // set on EngineEventDone
	MessageID         string             `json:"message_id,omitempty"`  // set on message_start
	Model             string             `json:"model,omitempty"`       // set on message_start
	StopReason        string             `json:"stop_reason,omitempty"` // set on message_delta

	// Sub-agent fields (set on subagent_start / subagent_end)
	AgentID       string   `json:"agent_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	AgentDesc     string   `json:"agent_desc,omitempty"`
	AgentType     string   `json:"agent_type,omitempty"`
	ParentAgentID string   `json:"parent_agent_id,omitempty"`
	Duration      int64    `json:"duration_ms,omitempty"`
	AgentStatus   string   `json:"agent_status,omitempty"` // for subagent_end: "completed", "error", "max_turns"
	DeniedTools   []string `json:"denied_tools,omitempty"` // tools denied during sub-agent execution

	// Task event fields (Phase 2+)
	TaskEvent     *TaskEvent         `json:"task_event,omitempty"`
	// Agent message fields (Phase 3+)
	AgentMsg      *AgentMessageEvent `json:"agent_msg,omitempty"`
	// Team event fields (Phase 5+)
	TeamEvent     *TeamEvent         `json:"team_event,omitempty"`
	// Sub-agent real-time streaming content (for subagent_event type)
	SubAgentEvent *SubAgentEventData `json:"subagent_event,omitempty"`
}

// TaskEvent carries task state change info.
type TaskEvent struct {
	TaskID     string `json:"task_id"`
	Subject    string `json:"subject"`
	Status     string `json:"status"`
	Owner      string `json:"owner,omitempty"`
	ActiveForm string `json:"active_form,omitempty"`
	ScopeID    string `json:"scope_id"`
}

// SubAgentEventData carries a sub-agent's real-time streaming content.
// This wraps the inner event so it doesn't interfere with the parent's
// message lifecycle in the EventMapper.
type SubAgentEventData struct {
	EventType string `json:"event_type"`           // inner event type: "text", "tool_start", "tool_end", etc.
	Text      string `json:"text,omitempty"`        // for text events
	ToolName  string `json:"tool_name,omitempty"`   // for tool events
	ToolInput string `json:"tool_input,omitempty"`  // for tool_start
	ToolUseID string `json:"tool_use_id,omitempty"` // for tool events
	IsError   bool   `json:"is_error,omitempty"`    // for tool_end errors
	Output    string `json:"output,omitempty"`      // for tool_end output
}

// AgentMessageEvent carries inter-agent message summary.
type AgentMessageEvent struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Summary string `json:"summary"`
	TeamID  string `json:"team_id,omitempty"`
}

// TeamEvent carries team lifecycle info.
type TeamEvent struct {
	TeamID     string   `json:"team_id"`
	TeamName   string   `json:"team_name"`
	Members    []string `json:"members,omitempty"`
	MemberName string   `json:"member_name,omitempty"`
	MemberType string   `json:"member_type,omitempty"`
}

// PermissionRequest is sent to the client when a tool execution needs approval.
type PermissionRequest struct {
	RequestID     string             `json:"request_id"`      // unique ID for correlating the response
	ToolName      string             `json:"tool_name"`
	ToolInput     string             `json:"tool_input"`
	Message       string             `json:"message"`         // human-readable description of what's being asked
	IsReadOnly    bool               `json:"is_read_only"`
	Options       []PermissionOption `json:"options"`         // available choices for the client to display
	PermissionKey string             `json:"permission_key"`  // session-allow granularity key (e.g. "Bash:git", "FileEdit:/src/main.go")
}

// PermissionOption describes one choice the client can present to the user.
type PermissionOption struct {
	Label string          `json:"label"` // display text, e.g. "Allow once"
	Scope PermissionScope `json:"scope"` // "once" or "session"
	Allow bool            `json:"allow"` // true=approve, false=deny
}

// PermissionScope controls how long a permission approval lasts.
type PermissionScope string

const (
	// PermissionScopeOnce approves the tool for this single invocation only.
	PermissionScopeOnce PermissionScope = "once"
	// PermissionScopeSession approves the tool for the rest of this session.
	// Subsequent calls to the same tool in the same session will auto-approve.
	PermissionScopeSession PermissionScope = "session"
)

// PermissionResponse is the client's answer to a PermissionRequest.
type PermissionResponse struct {
	RequestID string          `json:"request_id"`          // must match PermissionRequest.RequestID
	Approved  bool            `json:"approved"`
	Scope     PermissionScope `json:"scope,omitempty"`     // "once" (default) or "session"
	Message   string          `json:"message,omitempty"`   // optional reason for denial
}

// TerminalReason classifies why the query loop stopped.
// Mirrors the 10 terminal reasons from the TypeScript query.ts.
type TerminalReason string

const (
	TerminalCompleted          TerminalReason = "completed"            // LLM finished naturally (end_turn)
	TerminalAbortedStreaming   TerminalReason = "aborted_streaming"    // user cancelled during LLM streaming
	TerminalAbortedTools       TerminalReason = "aborted_tools"        // user cancelled during tool execution
	TerminalMaxTurns           TerminalReason = "max_turns"            // engine.max_turns reached
	TerminalPromptTooLong      TerminalReason = "prompt_too_long"      // context exceeds model limit after compaction
	TerminalBlockingLimit      TerminalReason = "blocking_limit"       // rate-limit or credit exhaustion
	TerminalModelError         TerminalReason = "model_error"          // unrecoverable LLM API error
	TerminalImageError         TerminalReason = "image_error"          // image processing failure
	TerminalStopHookPrevented  TerminalReason = "stop_hook_prevented"  // post-tool hook vetoed the stop
	TerminalHookStopped        TerminalReason = "hook_stopped"         // hook forced an early stop
)

// Terminal carries the reason and optional metadata for why a query ended.
type Terminal struct {
	Reason  TerminalReason `json:"reason"`
	Message string         `json:"message,omitempty"`
	Turn    int            `json:"turn"` // how many turns were executed
}
