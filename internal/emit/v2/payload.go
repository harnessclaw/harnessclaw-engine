package emitv2

// Typed payloads for each card_kind. Using concrete structs (rather than
// raw maps) gives us field-level documentation, JSON tag conventions, and
// compile-time guarantees that producers cannot misspell payload keys.
//
// All payload structs are pointer-friendly: pass &XxxPayload{...} to
// CardBuilder.Add / Set / Close. The builder marshals them as JSON in the
// Event.Payload field.

// ToolPhase classifies the runtime phase of a tool card before it
// reaches the terminal status carried on card.close. Phase transitions
// flow through card.set events on the tool card.
type ToolPhase string

const (
	PhasePlanning       ToolPhase = "planning"        // model 在拼 args
	PhasePlanningArgs   ToolPhase = "planning_args"   // args 已超 200B，发 progress
	PhaseQueued         ToolPhase = "queued"          // LLM stream 完成等调度
	PhasePermissionWait ToolPhase = "permission_wait" // 等用户授权
	PhaseExecuting      ToolPhase = "executing"       // executor 真正在跑
	// 终态走 ClosePayload.Status

	// PhaseNextRound 不是 tool card 上的 phase — 仅作为 CopyPicker 查询 key
	// 使用，文案落到 message card 的 Hint.Summary 上。声明在这里保持 picker
	// 接口一致。
	PhaseNextRound ToolPhase = "next_round_thinking"
)

// ----- card.add / card.set / card.close payloads (per card_kind) -----

// TurnPayload describes a single user-input → assistant-reply round.
type TurnPayload struct {
	TurnNo  int    `json:"turn_no,omitempty"`
	Channel string `json:"channel,omitempty"` // chat | voice | api
}

// MessagePayload describes one LLM reply.
type MessagePayload struct {
	Role       string `json:"role,omitempty"` // assistant | user
	Model      string `json:"model,omitempty"`
	StopReason string `json:"stop_reason,omitempty"` // end_turn | tool_use | max_tokens | error
}

// ToolPayload describes one tool invocation.
//
// Metadata carries any tool-specific extras the renderer may want
// (e.g. WebSearch puts `urls`/`query`/`result_count` here, Bash puts
// `exit_code`/`duration_ms`). The well-known keys `render_hint` /
// `language` / `file_path` are promoted to typed fields above and
// stripped from Metadata to avoid duplication; everything else flows
// through verbatim.
type ToolPayload struct {
	Name        string         `json:"name"`
	Target      string         `json:"target,omitempty"` // server | client
	Intent      string         `json:"intent,omitempty"` // model-supplied progress sentence
	Input       map[string]any `json:"input,omitempty"`
	Output      string         `json:"output,omitempty"`
	RenderHint  string         `json:"render_hint,omitempty"` // terminal | code | diff | search | ...
	Language    string         `json:"language,omitempty"`
	FilePath    string         `json:"file_path,omitempty"`
	Artifacts   []ArtifactRef  `json:"artifacts,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`

	// Phase 系列字段在 tool card 进入终态前由 card.set 事件流式更新。
	// Phase 是机器可读的枚举；PhaseHint 是引擎用文案库解析好的中文，
	// 前端直接渲染；PhaseBytes 只在 PhasePlanningArgs 时有效，承载
	// 流式累积的 tool_input 字节数（已经过 humanize 表达在 PhaseHint 里，
	// 这里保留原始数字仅用于开发者面板 / 调试视图）。
	Phase      ToolPhase `json:"phase,omitempty"`
	PhaseHint  string    `json:"phase_hint,omitempty"`
	PhaseBytes int       `json:"phase_bytes,omitempty"`
}

// AgentPayload describes a sub-agent session.
type AgentPayload struct {
	Name          string `json:"name,omitempty"`
	AgentType     string `json:"agent_type,omitempty"` // sync | async — runtime execution shape
	// SubagentType is the LLM-facing dispatch label (writer / researcher
	// / analyst / freelancer / etc.). Front-ends render this on the
	// agent card / metrics row so users can tell which worker did what;
	// AgentType alone returns "sync" for every leaf and is useless for
	// disambiguation in dashboards.
	SubagentType  string `json:"subagent_type,omitempty"`
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	TaskPrompt    string `json:"task_prompt,omitempty"` // full prompt the parent dispatched
	OutputSummary string `json:"output_summary,omitempty"`
	NumTurns      int    `json:"num_turns,omitempty"`
	DeniedTools   []string      `json:"denied_tools,omitempty"`
	Artifacts     []ArtifactRef `json:"artifacts,omitempty"`
	// LoadedSkills is set on subagent_start for skill-aware agents
	// (freelancer, or any fixed L3 enhanced with SearchSkill / LoadSkill
	// in AllowedTools). Empty for plain workers. Each entry is name +
	// version + source so the UI can render "loaded: docx@0.3 (preloaded)"
	// chips.
	LoadedSkills []LoadedSkillInfo `json:"loaded_skills,omitempty"`
}

// LoadedSkillInfo is the wire-side view of one preloaded skill on an
// agent card. Mirrors pkg/types/event.go's LoadedSkillInfo so the
// translator can pass through verbatim without a transform.
type LoadedSkillInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"` // "candidate" | "runtime"
}

// PlanPayload describes the orchestrator-produced task graph.
type PlanPayload struct {
	PlanID    string         `json:"plan_id"`
	Goal      string         `json:"goal,omitempty"`
	Strategy  string         `json:"strategy,omitempty"` // sequential | parallel | mixed
	Rationale string         `json:"rationale,omitempty"`
	Steps     []PlanStepInfo `json:"steps,omitempty"`
}

// PlanStepInfo is a per-step descriptor inside PlanPayload.Steps.
type PlanStepInfo struct {
	StepID            string   `json:"step_id"`
	SubagentType      string   `json:"subagent_type,omitempty"`
	DependsOn         []string `json:"depends_on,omitempty"`
	UserFacingTitle   string   `json:"user_facing_title,omitempty"`
	UserFacingSummary string   `json:"user_facing_summary,omitempty"`
}

// StepPayload describes one step of an executing plan.
type StepPayload struct {
	StepID        string        `json:"step_id"`
	SubagentType  string        `json:"subagent_type,omitempty"`
	Status        string        `json:"status,omitempty"` // queued | running
	InputSummary  string        `json:"input_summary,omitempty"`
	OutputSummary string        `json:"output_summary,omitempty"`
	Attempts      int           `json:"attempts,omitempty"`
	Deliverables  []string      `json:"deliverables,omitempty"`
	Artifacts     []ArtifactRef `json:"artifacts,omitempty"`
}

// ArtifactPayload describes a produced artifact (file/data).
type ArtifactPayload struct {
	ArtifactID  string `json:"artifact_id"`
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`     // file | data | image | ...
	MimeType    string `json:"mime_type,omitempty"`
	SizeBytes   int    `json:"size_bytes,omitempty"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`     // draft_email | report | summary
	URI         string `json:"uri,omitempty"`      // e.g. file:///tmp/xxx or artifact://...
	Version     int    `json:"version,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
}

// ArtifactRef is a lightweight reference embedded in tool/step/agent
// payloads. The full artifact is described by its own ArtifactPayload card.
type ArtifactRef struct {
	ArtifactID  string `json:"artifact_id"`
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	SizeBytes   int    `json:"size_bytes,omitempty"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`
}

// ThinkingPayload describes an extended-thinking block.
type ThinkingPayload struct {
	Length int `json:"length,omitempty"` // accumulated chars
}

// MemoryOpPayload describes a memory read/write or context compaction.
type MemoryOpPayload struct {
	Op      string `json:"op"` // read | write | compact
	Subject string `json:"subject,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
}

// BudgetPayload describes a budget warning or reset.
type BudgetPayload struct {
	Kind  string  `json:"kind"` // warning | exhausted | reset
	Spent *Budget `json:"spent,omitempty"`
	Limit *Budget `json:"limit,omitempty"`
}

// TodoPayload describes one TodoList item.
type TodoPayload struct {
	Subject    string `json:"subject"`
	Status     string `json:"status,omitempty"` // pending | in_progress | completed | cancelled
	Owner      string `json:"owner,omitempty"`
	ActiveForm string `json:"active_form,omitempty"`
}

// TeamPayload describes a co-spawn team.
type TeamPayload struct {
	TeamName string   `json:"team_name"`
	Members  []string `json:"members,omitempty"`
}

// SystemPayload describes a framework-emitted system-level notice
// (capability gap, configuration warning, etc.). Title / Summary /
// ActionHint render on the user-facing card; Topic is a stable
// machine identifier the client can branch on for business logic
// (deeplinks, telemetry, conditional UI). Known topic values are
// documented in docs/protocols/websocket.md §10.9.
type SystemPayload struct {
	Topic      string `json:"topic,omitempty"`
	Summary    string `json:"summary,omitempty"`
	ActionHint string `json:"action_hint,omitempty"`
}

// ----- card.close payload extension -----

// ClosePayload is what card.close events carry: a status, plus optional
// error/output/metrics. The Builder builds this automatically from
// status + options; producers don't construct it directly.
type ClosePayload struct {
	Status  Status     `json:"status"`
	Error   *ErrorInfo `json:"error,omitempty"`
	Inner   any        `json:"inner,omitempty"` // type-specific final payload (e.g. ToolPayload.Output)
}

// ----- card.append payload -----

// AppendPayload carries a streaming chunk. The Channel determines which
// content track of the card it accumulates into.
type AppendPayload struct {
	Channel     Channel `json:"channel"`
	Index       int     `json:"index,omitempty"`
	Chunk       string  `json:"chunk,omitempty"`        // for text / thinking
	PartialJSON string  `json:"partial_json,omitempty"` // for tool_input
}

// ----- card.tick payload -----

// TickPayload is the envelope for all throttled signal events. The Kind
// determines which Inner payload is meaningful.
type TickPayload struct {
	Kind  TickKind `json:"kind"`
	Inner any      `json:"inner,omitempty"`
}

// ProgressPayload is the inner payload for kind=progress.
type ProgressPayload struct {
	Stage          string  `json:"stage,omitempty"`
	ProgressPct    float64 `json:"progress_pct,omitempty"` // 0.0 — 1.0
	ItemsProcessed int     `json:"items_processed,omitempty"`
	ItemsTotal     int     `json:"items_total,omitempty"`
	Unit           string  `json:"unit,omitempty"`     // e.g. "pages", "files"
	ETAMs          int64   `json:"eta_ms,omitempty"`
}

// HeartbeatPayload is the inner payload for kind=heartbeat.
type HeartbeatPayload struct {
	Stage             string `json:"stage,omitempty"`
	UptimeMs          int64  `json:"uptime_ms,omitempty"`
	ActiveToolCardID  string `json:"active_tool_card_id,omitempty"`
}

// IntentPayload is the inner payload for kind=intent.
type IntentPayload struct {
	Intent string `json:"intent"`
}

// NotePayload is the inner payload for kind=note (debug/log annotation).
type NotePayload struct {
	Text     string   `json:"text"`
	Severity Severity `json:"severity,omitempty"`
}

// EscalationPayload is the inner payload for kind=escalation. Replaces
// v1's coordinator_react.go hack of sending escalation as text content.
type EscalationPayload struct {
	FromMode string `json:"from_mode"`
	ToMode   string `json:"to_mode"`
	Reason   string `json:"reason,omitempty"`
}

// ----- prompt.user payloads -----

// PromptUserPayload is the envelope for user-facing prompts. Kind
// determines which Inner payload is meaningful.
type PromptUserPayload struct {
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"` // permission | question | plan_review
	Inner     any    `json:"inner,omitempty"`
	TimeoutMs int64  `json:"timeout_ms,omitempty"`
}

// PermissionPromptPayload is the inner payload for kind=permission.
type PermissionPromptPayload struct {
	ToolName      string             `json:"tool_name"`
	ToolInput     string             `json:"tool_input,omitempty"`
	Message       string             `json:"message,omitempty"`
	IsReadOnly    bool               `json:"is_read_only,omitempty"`
	Options       []PermissionOption `json:"options"`
	PermissionKey string             `json:"permission_key,omitempty"`
}

// PermissionOption is one choice the user can pick.
type PermissionOption struct {
	Label string `json:"label"`
	Scope string `json:"scope"` // once | session
	Allow bool   `json:"allow"`
}

// QuestionPromptPayload is the inner payload for kind=question (replaces
// v1's askUserQuestion as a tool-call hack).
type QuestionPromptPayload struct {
	Question    string           `json:"question"`
	Options     []QuestionOption `json:"options,omitempty"`
	Multi       bool             `json:"multi,omitempty"`
	AllowCustom bool             `json:"allow_custom,omitempty"`
}

// QuestionOption is one selectable option in a question prompt.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PlanReviewPromptPayload is the inner payload for kind=plan_review.
type PlanReviewPromptPayload struct {
	PlanID             string             `json:"plan_id"`
	Goal               string             `json:"goal,omitempty"`
	Rationale          string             `json:"rationale,omitempty"`
	Steps              []PlanReviewStep   `json:"steps"`
	AvailableSubagents []string           `json:"available_subagents,omitempty"`
	RejectionReason    string             `json:"rejection_reason,omitempty"` // when this is a re-prompt after a rejected edit
}

// PlanReviewStep is one editable step in a plan review prompt.
type PlanReviewStep struct {
	ID           string   `json:"id"`
	SubagentType string   `json:"subagent_type,omitempty"`
	Description  string   `json:"description,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
}

// StepDecisionPromptPayload is the inner payload for kind=step_decision —
// the coordinator pauses on a hard step / plan failure and asks the user
// to pick continue / retry / cancel. Mirrors types.StepDecisionRequest
// on the wire side; engine→wire conversion lives in the channel
// translator.
type StepDecisionPromptPayload struct {
	// Scope: "step" or "plan".
	Scope           string `json:"scope"`
	StepID          string `json:"step_id,omitempty"`
	StepDescription string `json:"step_description,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Attempts        int    `json:"attempts,omitempty"`
	AllowRetry      bool   `json:"allow_retry,omitempty"`
}

// ----- prompt.reply payload -----

// PromptReplyPayload is the server's echo after a prompt is resolved.
type PromptReplyPayload struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"` // approved | denied | timeout | cancelled
	Reason    string `json:"reason,omitempty"`
}

// ----- session.event payload -----

// SessionPayload is the envelope for session-level events.
type SessionPayload struct {
	Kind  string `json:"kind"` // opened | updated | error | resumed | resume_failed
	Inner any    `json:"inner,omitempty"`
}

// SessionOpenedPayload is the inner payload for kind=opened.
type SessionOpenedPayload struct {
	ProtocolVersion string         `json:"protocol_version"`
	Model           string         `json:"model,omitempty"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
}

// SessionResumedPayload is the inner payload for kind=resumed.
type SessionResumedPayload struct {
	TraceID string `json:"trace_id"`
	FromSeq int64  `json:"from_seq"`
	ToSeq   int64  `json:"to_seq"`
}

// SessionResumeFailedPayload is the inner payload for kind=resume_failed.
type SessionResumeFailedPayload struct {
	TraceID string `json:"trace_id,omitempty"`
	Reason  string `json:"reason"` // events_expired | unknown_trace | session_not_found | not_implemented
}
