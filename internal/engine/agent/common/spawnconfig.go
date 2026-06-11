package common

import (
	"time"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// SpawnConfig describes how to create and run a sub-agent. It is a
// transitional carrier between the old agent.AgentSpawner API and the new
// scheduler.Runtime path. New code should prefer scheduler.SpawnParams
// directly; SpawnConfig still flows through Runtime.LLM and the
// browser_agent module while those callers are migrated.
type SpawnConfig struct {
	// Prompt is the task instruction injected as the first user message.
	Prompt string

	// AgentType controls tool filtering (sync, async, teammate, coordinator, custom).
	AgentType tool.AgentType

	// SubagentType selects the prompt profile (e.g. "plan", "freelancer").
	// Empty defaults to "freelancer".
	SubagentType string

	// Description is a short human-readable summary (3-5 words) for observability.
	Description string

	// Name is an optional identifier for the sub-agent (used in messaging/addressing).
	Name string

	// Model overrides the LLM model for this sub-agent. Empty inherits from parent.
	Model string

	// MaxTurns caps the sub-agent's query loop iterations.
	// Zero means use the default (parent's MaxTurns / 2, minimum 5).
	MaxTurns int

	// Timeout is the maximum wall-clock duration for the entire sub-agent execution.
	// Zero means no timeout beyond the parent context.
	Timeout time.Duration

	// Fork controls whether the sub-agent inherits the parent's conversation context.
	// When true, the parent's messages are deep-copied into the sub-agent session
	// and the system prompt bytes are preserved for prompt cache stability.
	// When false (default), the sub-agent starts with a blank session.
	Fork bool

	// ContextSummary provides a compressed context summary for "distill" mode.
	// When non-empty and Fork is false, this summary is prepended to the Prompt
	// as background context, giving the sub-agent essential information without
	// the full conversation history (which would dilute attention) or a blank
	// slate (which would lack necessary context).
	//
	// The parent agent or coordinator should produce this summary by extracting
	// only the information relevant to the sub-agent's specific task.
	ContextSummary string

	// ParentSessionID is the parent's session ID, used for session ID generation
	// and context inheritance in fork mode.
	ParentSessionID string

	// ParentAgentID is the agent_id of the direct parent that initiated
	// this spawn. Used by sub-agent modules to populate the wire envelope's
	// ParentAgentID field (see subagent.start event schema). "main" when
	// emma is the parent; otherwise the parent's agent_id (e.g., scheduler
	// passes its own ID when dispatching plan_agent in plan-mode).
	//
	// Empty allowed during legacy code paths; new modules treat empty as
	// "no explicit parent" and emit a warn log.
	ParentAgentID string

	// RootSessionID is the top-level user-facing session id (emma's main
	// session). Sub-agents propagate this through their own spawns so the
	// root session's metrics tracker sees every descendant's token spend
	// — solves the "GET /sessions/{emma_session_id}/metrics shows only
	// immediate children" gap when L2 dispatches to L3 via plan-mode
	// (which uses sess.ID as ParentSessionID, hiding L3 from emma's view).
	//
	// Empty defaults to ParentSessionID (the L2 case where the immediate
	// parent IS the root, e.g. scheduler spawned directly by emma).
	RootSessionID string

	// InputPaths lists upstream task output paths this spawn will read.
	// Used by the framework to render the <task-inputs> preamble and to
	// compute ReadScope. Absolute paths.
	InputPaths []string

	// ReadScope restricts File* tool reads. Absolute path prefixes; empty
	// means no restriction (legacy compat). Engine derives ReadScope from
	// {own task dir, InputPaths, plan.json}.
	ReadScope []string

	// WriteScope restricts File* tool writes. Absolute path prefixes; empty
	// means no restriction (legacy compat). Typically {own task dir}.
	WriteScope []string

	// SystemPromptOverride replaces the sub-agent's generated system prompt.
	// Used in fork mode to preserve the parent's prompt cache prefix.
	SystemPromptOverride string

	// AllowedSkills restricts which skills the sub-agent can invoke via SkillTool.
	// When non-empty, only skills in this list are available; the system prompt
	// skill listing is also filtered accordingly.
	// When empty, all skills are accessible (default behavior).
	AllowedSkills []string

	// ParentOut is the parent query loop's event output channel.
	// When set, SpawnSync emits subagent.start/end events on this channel
	// so they reach the WebSocket client.
	ParentOut chan<- types.EngineEvent

	// ExpectedOutputs declares what artifacts the sub-agent must deliver.
	// When non-empty:
	//   - the framework injects an `<expected-outputs>` block into the
	//     L3 task prompt so the LLM sees its contract
	//   - SubmitTaskResult schema is restricted: artifacts.minItems is
	//     set to count(required), and each submitted artifact must
	//     declare a role drawn from this list
	//   - the loop refuses to terminate until SubmitTaskResult passes
	//     server-side validation (doc §3 mechanisms M3/M4)
	// Empty means "no contract" — the L3 may end_turn without submitting,
	// preserving backward compatibility for simple tasks.
	ExpectedOutputs []types.ExpectedOutput

	// TaskID is the orchestrator-assigned identifier for this work unit.
	// Stamped into producer.task_id on every artifact this sub-agent
	// writes, and used by SubmitTaskResult validation to confirm the
	// submitted artifacts originated in THIS task (preventing failure
	// mode #8 — submitting a foreign artifact's id). Empty when the
	// dispatcher didn't assign one (legacy path).
	TaskID string

	// TaskStartedAt is the timestamp the orchestrator considers this task
	// to have begun. SubmitTaskResult rejects artifacts whose CreatedAt
	// is before this — guarding against the "claim a pre-existing
	// artifact as my output" form of failure #8.
	// Zero means "no temporal check" (legacy path).
	TaskStartedAt time.Time

	// ParentStepID, when non-empty, names the plan / orchestrate step
	// this sub-agent is fulfilling. Carried through to the
	// EngineEventSubAgentStart wire event so the channel translator can
	// root the agent card under the step card. Without this routing, the
	// step card's orphan watchdog sits without heartbeats for the entire
	// duration of the dispatched sub-agent and gets killed mid-flight.
	// Empty for non-plan dispatches (direct AskUserQuestion-style spawns,
	// L1-emitted spawns) — the translator falls back to the legacy
	// parent (tool / message / turn).
	ParentStepID string

	// Inputs carries structured key-value data for the sub-agent's task.
	// When the target AgentDefinition has an InputSchema, SpawnSync
	// validates Inputs against it before spawning — a validation failure
	// returns an error immediately, before any LLM call is made. When
	// InputSchema is absent or Inputs is nil, no validation is performed.
	//
	// Values here are NOT automatically injected into the Prompt string;
	// the caller is responsible for building a Prompt that references the
	// structured inputs if the agent should see them as natural language.
	// Inputs is primarily a machine-readable contract check at the
	// dispatcher boundary.
	Inputs map[string]any

	// CoordinatorMode optionally pins the L2 coordinator mode for this
	// spawn. Only meaningful for coordinator-tier agents (scheduler,
	// Plan, Explore, etc.); ignored for TierSubAgent which always runs
	// the strict L3 driver.
	//
	// Allowed values mirror engine.CoordinatorMode: "react" (default),
	// "plan" (requires explicit opt-in until Plan implementation lands),
	// "" (registry resolves to default).
	//
	// Wiring path: WebSocket clients pass coordinator_mode at session /
	// turn level; the API layer threads it through ProcessMessage onto
	// SpawnConfig when emma dispatches scheduler. Unknown values
	// degrade gracefully to ReAct with a warn log — bad client input
	// must never crash the spawn.
	//
	// String type (not engine.CoordinatorMode) keeps the agent package
	// dependency-light: agent shouldn't import engine.
	CoordinatorMode string
}
