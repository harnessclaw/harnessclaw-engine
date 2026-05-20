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
	// Model carries the LLM model that actually served this call. Filled
	// on StreamEventMessageEnd by the provider adapter so downstream
	// observers (notably sessionstats) can attribute usage per-model
	// without the engine having to plumb model identity through
	// ChatRequest.Model — which is empty in the common case (engine lets
	// the provider's configured default win).
	Model string `json:"model,omitempty"`
	// Reasoning is the assistant's accumulated thinking-mode output
	// captured across the stream's delta.reasoning fields and folded
	// into the terminal MessageEnd event. Engine forwards it onto the
	// outgoing assistant Message so the next request can echo it back
	// (DeepSeek thinking models reject requests where it's missing).
	Reasoning string `json:"reasoning,omitempty"`
	Error     error  `json:"-"`
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

	// EngineEventLLMHeartbeat fires periodically while an LLM call is
	// in flight but no chunks have arrived (or are arriving slowly).
	// Keeps the lifecycle watchdog from synthesising a failed close on
	// the surrounding step / agent / message cards just because the
	// upstream model is slow. Translator maps it to a
	// card.tick(kind=heartbeat) on the current agent or message card,
	// whose Tracker.Touch walks the parent chain and resets every
	// ancestor's orphan deadline. AgentID may be empty for the L1
	// main-loop call; the translator falls back to the active message
	// card in that case.
	EngineEventLLMHeartbeat      EngineEventType = "llm_heartbeat"

	// EngineEventLLMRetry fires from inside retry.Retryer just before it
	// sleeps for the backoff delay between two attempts. Carries the
	// attempt index, remaining budget, planned delay, and the classified
	// error that triggered the retry. Without this the front-end has no
	// way to distinguish "model is thinking slowly" (heartbeats only)
	// from "we hit a 5xx and are about to retry" — both look like the
	// same silent wait. Translator renders it as a card.tick(kind=note)
	// on the active agent/message card with a human-readable status.
	EngineEventLLMRetry          EngineEventType = "llm_retry"
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
	EngineEventPlanProposed    EngineEventType = "plan_proposed"     // plan coordinator → user: please review/edit before exec
	EngineEventPlanApproved    EngineEventType = "plan_approved"     // user → plan coordinator: ok continue (with optional edits)

	// EngineEventStepDecisionRequested fires when the Scheduler /
	// PlanCoordinator hits a failure that previously would have been
	// silently fallback'd (transient retries exhausted, re-plans
	// exhausted, max-turns hit). Instead of dropping work the user
	// already paid for, the coordinator pauses, emits this event so the
	// channel can ask the user, and blocks on the response. User picks
	// continue (skip) / retry / cancel — see StepDecisionResponse.
	EngineEventStepDecisionRequested EngineEventType = "step_decision_requested"

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

	// EngineEventSystemNotice fires when the framework wants to surface
	// a non-error system-level notification to the user (e.g. a
	// configuration gap such as "all search tool backends are
	// disabled, sub-agent quality may suffer"). Translator maps it to
	// a CardSystem card with Hint.Icon controlling severity styling.
	EngineEventSystemNotice EngineEventType = "system_notice"

	// EngineEventToolPlanning fires when SSE stream first surfaces a
	// tool_use block with a known name. Translator opens the CardTool
	// early (with WithoutLifecycle) so the user sees the card during
	// args streaming, not only after executor dispatch.
	EngineEventToolPlanning EngineEventType = "tool_planning"

	// EngineEventToolPlanningProgress fires throttled (50ms) while
	// tool_input args accumulate beyond 200 bytes. Translator emits
	// card.set with Phase=planning_args + PhaseBytes counter.
	EngineEventToolPlanningProgress EngineEventType = "tool_planning_progress"

	// EngineEventToolQueued fires when LLM MessageEnd arrives, before
	// dispatchToolBatch hands off to executor. Translator sets
	// Phase=queued on the tool card.
	EngineEventToolQueued EngineEventType = "tool_queued"

	// EngineEventToolPlanningRetract fires when callLLM enters retry
	// (onRetry callback). Translator closes all CardTools that were
	// opened by planning but not yet upgraded to executing — typical
	// path is card.close(status=cancelled) with a "model retry — superseded"
	// error message.
	EngineEventToolPlanningRetract EngineEventType = "tool_planning_retract"

	// EngineEventNextRoundThinking fires after all tool results have
	// been appended to the session, just before the next callLLM kicks
	// off. Translator pre-opens a new message card with Hint.Summary
	// populated by the copy library ("正在解读结果"). When the LLM
	// stream's first byte lands, the hint gives way to streaming text.
	EngineEventNextRoundThinking EngineEventType = "next_round_thinking"
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

	// ErrorDetails carries provider/router-specific structured context
	// for EngineEventError frames. The channel translator pulls keys it
	// knows (user_message / error_code / model / rejected_modalities)
	// into typed wire fields and ignores the rest. Used by the
	// multimodal gate to forward the rich UnsupportedModalityError
	// payload to the client.
	ErrorDetails      map[string]any     `json:"error_details,omitempty"`
	MessageID         string             `json:"message_id,omitempty"`  // set on message_start
	Model             string             `json:"model,omitempty"`       // set on message_start
	StopReason        string             `json:"stop_reason,omitempty"` // set on message_delta

	// Intent is the model-supplied progress sentence on agent_intent events
	// ("正在搜 vLLM 论文"). Stays empty for other event types.
	Intent string `json:"intent,omitempty"`

	// Bytes carries the cumulative tool_input byte count on
	// EngineEventToolPlanningProgress events. Zero for all other types.
	Bytes int `json:"bytes,omitempty"`

	// Sub-agent fields (set on subagent_start / subagent_end)
	AgentID       string   `json:"agent_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	AgentDesc     string   `json:"agent_desc,omitempty"`     // short label, 3-5 words ("调研 LLM 推理")
	AgentTask     string   `json:"agent_task,omitempty"`     // full task prompt the parent dispatched (set on subagent_start so the user can see what each L3 was actually asked to do)
	AgentType     string   `json:"agent_type,omitempty"`
	ParentAgentID string   `json:"parent_agent_id,omitempty"`
	// ParentStepID, on subagent_start, names the plan / orchestrate step
	// that dispatched this sub-agent. Channel translators use it to root
	// the agent card under the step card so the step's orphan watchdog
	// receives heartbeats from inner agent activity. Empty for non-plan
	// dispatches (legacy parent resolution applies).
	ParentStepID  string   `json:"parent_step_id,omitempty"`
	Duration      int64    `json:"duration_ms,omitempty"`
	AgentStatus   string   `json:"agent_status,omitempty"` // for subagent_end: "completed", "error", "max_turns"
	DeniedTools   []string `json:"denied_tools,omitempty"` // tools denied during sub-agent execution

	// SubagentType is the LLM-facing dispatch label — writer / researcher
	// / analyst / developer / freelancer / etc. Distinct from AgentType
	// (sync / coordinator), which only describes the runtime execution
	// shape. Front-ends rendering "which sub-agent ran this" need
	// SubagentType to actually distinguish workers; AgentType is mostly
	// noise to end users (every leaf worker is "sync").
	// Set on subagent_start; carried through subagent_end for symmetry.
	SubagentType string `json:"subagent_type,omitempty"`

	// LoadedSkills surfaces user-installed skills hydrated into a
	// sub-agent's spawn context. Populated on subagent_start for any
	// agent whose definition opts into skill self-management
	// (freelancer always; fixed L3s when they declare SearchSkill etc.
	// in AllowedTools). Empty when the agent doesn't carry any.
	// Front-ends render these as chips on the agent card so the user
	// can see "this freelancer started with docx skill preloaded"
	// without diving into server logs.
	LoadedSkills []LoadedSkillInfo `json:"loaded_skills,omitempty"`

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

	// Artifacts is the list of cross-agent artifacts surfaced by this event.
	// Doc §10: events carry references (lightweight metadata + ID), never
	// the artifact content itself. Populated on:
	//   - tool_end: a single ref when ArtifactWrite ran in this call.
	//   - subagent_end: aggregated refs of every artifact this sub-agent
	//     produced during its run, so the UI can render a single card listing
	//     all outputs.
	// Empty / omitted on every other event type.
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`

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

	// PlanProposal carries a plan that needs user review/edit before
	// the coordinator continues. Set on plan_proposed events; nil on
	// every other event type. Pairs with PlanResponse on the
	// client→server return path (see types.IncomingMessage).
	PlanProposal *PlanProposal `json:"plan_proposal,omitempty"`

	// StepDecision carries a failure-decision request from the
	// coordinator. Set on step_decision_requested events. Pairs with
	// StepDecisionResponse on the client→server return path.
	StepDecision *StepDecisionRequest `json:"step_decision,omitempty"`

	// LLMRetry carries the retry status emitted from inside the LLM
	// retry loop just before backoff sleep. Set on EngineEventLLMRetry;
	// nil on every other event type.
	LLMRetry *LLMRetryInfo `json:"llm_retry,omitempty"`

	// SystemNotice carries a framework-emitted system-level notification
	// payload. Set on EngineEventSystemNotice events; nil on every
	// other event type. Translator routes to CardSystem.
	SystemNotice *SystemNotice `json:"system_notice,omitempty"`
}

// LLMRetryInfo describes one retry decision inside the LLM Retryer. The
// numbers follow human-friendly 1-indexed semantics ("attempt 2 of 10")
// rather than the Retryer's internal 0-indexed loop counter so the wire
// frame can be rendered directly.
type LLMRetryInfo struct {
	// Attempt is the 1-indexed attempt number that just failed. Next
	// retry will be Attempt+1.
	Attempt int `json:"attempt"`

	// MaxRetries is the configured retry ceiling — i.e., the maximum
	// number of additional attempts after the initial call. Front-end
	// can compute "x / max+1" total attempts.
	MaxRetries int `json:"max_retries"`

	// DelayMs is how long the Retryer will sleep before the next
	// attempt. UI can show a countdown if it wants.
	DelayMs int64 `json:"delay_ms"`

	// ErrorType is the classified error kind from retry.APIErrorType
	// ("rate_limit", "overloaded", "server_error", "network_error", ...).
	// Empty if the classifier couldn't tag it.
	ErrorType string `json:"error_type,omitempty"`

	// StatusCode is the upstream HTTP status when available (0 for
	// non-HTTP errors like ECONNRESET).
	StatusCode int `json:"status_code,omitempty"`

	// Message is a short human-readable summary, suitable to render in
	// a note card on the wire.
	Message string `json:"message,omitempty"`
}

// SystemNotice carries the payload for EngineEventSystemNotice events.
// Topic is a short machine identifier for log/audit ("search_capability_gap");
// Title / Summary / ActionHint render on the user-facing card.
// Icon controls the front-end styling (empty defaults to registry "info").
type SystemNotice struct {
	Topic      string `json:"topic"`
	Title      string `json:"title"`
	Summary    string `json:"summary,omitempty"`
	ActionHint string `json:"action_hint,omitempty"`
	Icon       string `json:"icon,omitempty"`
}

// PlanProposal is the structured plan a coordinator presents to the user
// for review. The user may approve as-is, edit the steps, or reject.
//
// Why this is a separate struct rather than reusing PlanEvent: PlanEvent
// is observability-shaped (status / strategy / tasks for UI rendering)
// — it doesn't carry the editable per-step prompts. PlanProposal is the
// interactive contract.
type PlanProposal struct {
	// PlanID is server-generated, used by the client to correlate
	// plan.response back to the right pending request. Format:
	// "pln_<hex>".
	PlanID string `json:"plan_id"`

	// AgentID is the L2 coordinator instance asking for approval. The
	// client uses it to attribute the proposal to a specific agent
	// timeline entry.
	AgentID string `json:"agent_id,omitempty"`

	// Goal echoes the natural-language task description so the user
	// can sanity-check before approving step decomposition.
	Goal string `json:"goal,omitempty"`

	// Rationale is a one-line explanation from the Planner (e.g.
	// "research+write pattern detected"). Helpful for UI but not
	// load-bearing.
	Rationale string `json:"rationale,omitempty"`

	// Steps is the editable plan body. Order matters (topological).
	Steps []ProposedStep `json:"steps"`

	// AvailableSubagents is the whitelist of valid SubagentType values
	// for advanced clients that want to override the executor decision.
	// Server-side validation rejects any step whose SubagentType is set
	// AND outside this list. Default-shaped clients ignore it — the
	// server resolves the executor at dispatch time via SubagentResolver.
	//
	// Renamed from AvailableSkills (v1.16) to disambiguate from
	// AgentDefinition.Skills (capability tags, different concept).
	AvailableSubagents []string `json:"available_subagents,omitempty"`

	// TimeoutMs caps how long the server will wait for a response. 0
	// means "no timeout" (wait until either user responds or session
	// closes). Clients should render an explicit timer when non-zero.
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// ProposedStep is one editable step in a PlanProposal. Fields mirror
// engine.PlanStep but with JSON tags so the wire shape is stable
// independent of the engine's internal type.
//
// SubagentType is OPTIONAL on the wire (v1.16+): standard front-ends
// don't render it; the server's SubagentResolver picks the executor at
// dispatch time. Advanced clients may still set it to lock a specific
// L3, in which case the value must appear in PlanProposal.AvailableSubagents.
type ProposedStep struct {
	ID           string   `json:"id"`
	SubagentType string   `json:"subagent_type,omitempty"`
	Description  string   `json:"description,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
}

// PlanResponse is what the client sends back via plan.response after the
// user reviews a PlanProposal. Carried on types.IncomingMessage.
type PlanResponse struct {
	// PlanID identifies which pending proposal this responds to. Must
	// match the PlanID the server emitted; mismatches are dropped with
	// a warn log.
	PlanID string `json:"plan_id"`

	// Approved=true continues execution with UpdatedSteps (or original
	// if UpdatedSteps is nil). Approved=false cancels the run; the
	// coordinator falls back to a graceful degraded result.
	Approved bool `json:"approved"`

	// UpdatedSteps optionally replaces the proposed step list. nil /
	// empty means "use the proposal as-is". Server validates the
	// updated plan structure (no cycles, valid skills) before
	// continuing — invalid edits return Approved=false with an error
	// message in the next plan.proposed (re-plan trip).
	UpdatedSteps []ProposedStep `json:"updated_steps,omitempty"`

	// Reason is an optional human-readable rejection / edit comment.
	// Surfaced in coordinator logs only.
	Reason string `json:"reason,omitempty"`
}

// StepDecisionRequest is what the coordinator presents to the user when
// a step / plan path has hit a hard failure that previously fell back
// silently (transient retries exhausted, re-plans exhausted, sub-agent
// max turns, planner JSON validation cap). Lets the user decide whether
// the run should keep going, retry the failing piece, or stop.
type StepDecisionRequest struct {
	// RequestID is server-generated; the client echoes it back in
	// StepDecisionResponse so we can route the answer.
	RequestID string `json:"request_id"`

	// AgentID identifies the L2 coordinator instance asking. Lets the
	// UI attribute the prompt to the right agent timeline entry.
	AgentID string `json:"agent_id,omitempty"`

	// Scope describes WHAT failed: "step" (one step in a plan), "plan"
	// (re-plans exhausted / planner couldn't produce). Drives the
	// vocabulary of the rendered prompt.
	Scope string `json:"scope"`

	// StepID / StepDescription are populated when Scope=="step". Empty
	// for plan-scope failures.
	StepID          string `json:"step_id,omitempty"`
	StepDescription string `json:"step_description,omitempty"`

	// Reason is the one-line failure summary the coordinator built from
	// the StepResult / Planner error. Shown verbatim in the UI.
	Reason string `json:"reason,omitempty"`

	// Attempts is how many times the coordinator already tried this
	// piece before asking. Helps the user judge whether retry is likely
	// to help.
	Attempts int `json:"attempts,omitempty"`

	// AllowRetry tells the client whether the "retry" decision is
	// available. False for failures that can't be retried at this
	// level (e.g. planner JSON repeatedly invalid).
	AllowRetry bool `json:"allow_retry,omitempty"`
}

// StepDecisionResponse carries the user's decision after a
// step_decision_requested prompt. Carried on types.IncomingMessage.
type StepDecisionResponse struct {
	// RequestID identifies which pending decision this responds to.
	RequestID string `json:"request_id"`

	// Decision is one of:
	//   "continue" — accept the failure, mark the affected step skipped
	//                (or move past the failed plan stage), keep running.
	//   "retry"    — try the failing piece one more time. Coordinator
	//                runs the same step / re-plan again.
	//   "cancel"   — stop the whole run, route to fallback aggregation.
	Decision string `json:"decision"`

	// Note is an optional free-form comment. Surfaced in coordinator
	// logs / fallback summary so the user's reasoning makes it into the
	// audit trail.
	Note string `json:"note,omitempty"`
}

// Step decision values, exported so engine and channel code share the
// same vocabulary. Anything outside this set is treated as "cancel".
const (
	StepDecisionContinue = "continue"
	StepDecisionRetry    = "retry"
	StepDecisionCancel   = "cancel"

	StepDecisionScopeStep = "step"
	StepDecisionScopePlan = "plan"
)

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
// LoadedSkillInfo is the wire shape of one entry in EngineEvent.LoadedSkills.
// Kept narrow on purpose — name + version is what front-ends need to
// render a "loaded: docx@0.3" chip; the full SkillCard with description
// / when_to_use stays server-internal.
type LoadedSkillInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Source is "candidate" when the L2 dispatcher predeclared this
	// skill in candidate_skills, or "runtime" when the L3 itself called
	// LoadSkill mid-loop. The front-end shows them with different
	// visual treatment so the user can spot "L2 hand-fed this" vs
	// "the agent fetched it itself".
	Source string `json:"source,omitempty"`
}

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

	// Artifacts surfaces the artifact produced by this tool call (when
	// the wrapped event was a tool_end carrying ArtifactRef on the wire).
	// Forwarded so the parent layer (and its UI) can render produced
	// artifacts per-tool, mirroring what subagent_end shows aggregated.
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
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
	ToolUseID     string             `json:"tool_use_id"`     // correlate with the open tool card
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
	TerminalUnsupportedModality TerminalReason = "unsupported_modality" // model can't accept a content block's modality
)

// Terminal carries the reason and optional metadata for why a query ended.
type Terminal struct {
	Reason  TerminalReason `json:"reason"`
	Message string         `json:"message,omitempty"`
	Turn    int            `json:"turn"` // how many turns were executed
}
