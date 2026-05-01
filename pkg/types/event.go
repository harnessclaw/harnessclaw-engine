package types

import "harnessclaw-go/internal/emit"

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
	// AgentIntent fires immediately before a tool executes, carrying the
	// model-supplied "intent" field that every tool's input schema now
	// requires. It gives the user a per-call progress sentence ("正在搜索
	// vLLM 论文") without depending on prompt-side cooperation — the JSON
	// schema validator forces the model to fill `intent` before the call
	// is even dispatched. See ToolPool.Schemas / ToolExecutor.executeSingle.
	EngineEventAgentIntent       EngineEventType = "agent_intent"

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

	// Deliverable: file produced by sub-agent
	EngineEventDeliverable     EngineEventType = "deliverable"       // sub-agent wrote a deliverable file

	// Phase 5: Teams
	EngineEventTeamCreated     EngineEventType = "team_created"      // team created
	EngineEventTeamMemberJoin  EngineEventType = "team_member_join"  // member joined
	EngineEventTeamMemberLeft  EngineEventType = "team_member_left"  // member left
	EngineEventTeamDeleted     EngineEventType = "team_deleted"      // team dissolved

	// Trace lifecycle (one user-input → assistant-reply round).
	// Emitted by the engine entry point (QueryEngine.ProcessMessage) at the
	// boundary of a request — all other events for that request are nested
	// between the started/finished pair.
	EngineEventTraceStarted  EngineEventType = "trace_started"
	EngineEventTraceFinished EngineEventType = "trace_finished"
	EngineEventTraceFailed   EngineEventType = "trace_failed"

	// Plan lifecycle (orchestrator role). Emitted by PlanExecutor when a
	// validated plan begins / finishes. Plans are the unit of work for the
	// orchestrator; clients render them as a parent task with children.
	EngineEventPlanCreated   EngineEventType = "plan_created"
	EngineEventPlanUpdated   EngineEventType = "plan_updated"
	EngineEventPlanCompleted EngineEventType = "plan_completed"
	EngineEventPlanFailed    EngineEventType = "plan_failed"

	// Step dispatch / completion (orchestrator per-step). Emitted by the
	// PlanExecutor for each step in the plan as it transitions through
	// dispatched → started → completed/failed/skipped.
	//
	// NOTE: deliberately NOT named task_*. The user-facing TodoList in
	// §6.8 owns the task.* namespace; reusing it would make a one-level
	// type switch ambiguous on the client. See websocket.md v1.11
	// changelog for the rename rationale.
	EngineEventStepDispatched EngineEventType = "step_dispatched"
	EngineEventStepStarted    EngineEventType = "step_started"
	EngineEventStepProgress   EngineEventType = "step_progress"
	EngineEventStepCompleted  EngineEventType = "step_completed"
	EngineEventStepFailed     EngineEventType = "step_failed"
	EngineEventStepSkipped    EngineEventType = "step_skipped"

	// AgentHeartbeat is emitted by long-running agents to prove they are
	// still alive. Clients that expect a started→finished pair within a
	// time budget watch for missing heartbeats to surface stuck agents.
	EngineEventAgentHeartbeat EngineEventType = "agent_heartbeat"
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

	// Intent is the model-supplied progress sentence on agent_intent events
	// ("正在搜 vLLM 论文"). Stays empty for other event types.
	Intent string `json:"intent,omitempty"`

	// Sub-agent fields (set on subagent_start / subagent_end)
	AgentID       string   `json:"agent_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	AgentDesc     string   `json:"agent_desc,omitempty"`     // short label, 3-5 words ("调研 LLM 推理")
	AgentTask     string   `json:"agent_task,omitempty"`     // full task prompt the parent dispatched (set on subagent_start so the user can see what each L3 was actually asked to do)
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
	// Deliverable file produced by a sub-agent (for deliverable type)
	Deliverable   *Deliverable       `json:"deliverable,omitempty"`

	// --- Emit envelope/display/metrics (optional). Filled by the engine
	// at emit time so observers can route, render, and bill without
	// parsing per-type payloads. See internal/emit for the contract. ---
	Envelope *emit.Envelope `json:"envelope,omitempty"`
	Display  *emit.Display  `json:"display,omitempty"`
	Metrics  *emit.Metrics  `json:"metrics,omitempty"`

	// PlanEvent carries plan lifecycle data (plan_created / plan_updated /
	// plan_completed). Includes the task graph so clients can render the
	// full plan hierarchy in one event.
	PlanEvent *PlanEvent `json:"plan_event,omitempty"`

	// TaskDispatch carries per-step dispatch / completion / failure data
	// for the orchestrate (L2) layer. Identifies which step ran which
	// sub-agent and exposes the summary for UI rendering.
	TaskDispatch *TaskDispatch `json:"task_dispatch,omitempty"`
}

// PlanEvent carries an L2 plan and its lifecycle status for plan_*
// events. Status values: "created", "updated", "completed", "failed".
type PlanEvent struct {
	PlanID   string         `json:"plan_id"`
	Goal     string         `json:"goal,omitempty"`
	Strategy string         `json:"strategy,omitempty"` // "sequential" | "parallel" | "mixed"
	Status   string         `json:"status"`             // "created" | "updated" | "completed" | "failed"
	Tasks    []PlanTaskInfo `json:"tasks,omitempty"`    // initial task graph at plan_created
}

// PlanTaskInfo describes one step in a plan, including its dependency
// graph and a user-facing title/summary for UI rendering.
type PlanTaskInfo struct {
	TaskID            string   `json:"task_id"`
	SubagentType      string   `json:"subagent_type"`
	DependsOn         []string `json:"depends_on,omitempty"`
	UserFacingTitle   string   `json:"user_facing_title,omitempty"`
	UserFacingSummary string   `json:"user_facing_summary,omitempty"`
}

// TaskDispatch carries the data emitted on step_dispatched /
// step_started / step_completed / step_failed / step_skipped (and
// reused for plan_failed). The fields populated depend on the event:
//
//	dispatched: TaskID, SubagentType, InputSummary
//	started:    TaskID, AgentID
//	completed:  TaskID, OutputSummary, Attempts, Deliverables
//	failed:     TaskID, ErrorType, ErrorCode, Error, UserMessage, Retryable, Attempts
//	skipped:    TaskID, Reason
//
// Naming preserved for backwards compatibility with internal callers;
// the wire payload uses step_id rather than task_id (see protocol.go).
type TaskDispatch struct {
	TaskID        string   `json:"task_id"`
	SubagentType  string   `json:"subagent_type,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	InputSummary  string   `json:"input_summary,omitempty"`
	OutputSummary string   `json:"output_summary,omitempty"`
	Attempts      int      `json:"attempts,omitempty"`
	// ErrorType is the controlled enum string from emit.ErrorType
	// (e.g. "tool_timeout"). Required on failed events.
	ErrorType string `json:"error_type,omitempty"`
	// ErrorCode is the free-form machine-readable subtype (e.g.
	// "BASH_TIMEOUT"). Optional, scoped to the producing component.
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`        // developer-facing message
	UserMessage string `json:"user_message,omitempty"` // user-facing fallback for L1
	Retryable bool   `json:"retryable,omitempty"`
	Reason    string `json:"reason,omitempty"` // for step_skipped
	Deliverables []string `json:"deliverables,omitempty"` // file paths produced
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
//
// EventType drives which fields are meaningful:
//   - "tool_start"    — ToolName, ToolInput, ToolUseID
//   - "tool_end"      — ToolName, ToolUseID, Output, IsError
//   - "intent"        — ToolName, ToolUseID, Intent (model-supplied progress
//                       sentence emitted just before a tool runs; lets the
//                       client render "researcher 正在搜索 vLLM 论文" with
//                       no prompt-side cooperation since the JSON schema
//                       requires `intent` on every tool call)
//
// Reserved (not forwarded today, kept so message_id/error_message etc. don't
// get re-invented later):
//   - "message_start" / "message_delta" / "message_stop" — LLM call lifecycle;
//     intentionally suppressed because tokens / stop_reason are technical
//     metrics, not task-level information
//   - "error"        — sub-agent internal failures (TODO: surface these so
//                      LLM stream errors stop being invisible)
//   - "text"         — emma owns user-facing prose
type SubAgentEventData struct {
	EventType string `json:"event_type"`            // inner event type
	Text      string `json:"text,omitempty"`        // for text events (not forwarded today)
	ToolName  string `json:"tool_name,omitempty"`   // for tool / intent events
	ToolInput string `json:"tool_input,omitempty"`  // for tool_start
	ToolUseID string `json:"tool_use_id,omitempty"` // for tool / intent events
	IsError   bool   `json:"is_error,omitempty"`    // for tool_end errors
	Output    string `json:"output,omitempty"`      // for tool_end output

	// Intent is the model-supplied progress sentence on "intent" events,
	// extracted from the required `intent` field of the tool's input.
	Intent string `json:"intent,omitempty"`

	// Reserved — populated by future event types. Kept here so we don't
	// mint another wrapper struct when message lifecycle / error
	// forwarding is enabled.
	MessageID    string `json:"message_id,omitempty"`
	Model        string `json:"model,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
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
