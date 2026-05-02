// Package agent defines the interfaces and types for multi-agent orchestration.
// It sits between the tool layer (which needs to spawn sub-agents) and the
// engine layer (which implements the actual query loop), breaking the circular
// dependency via the AgentSpawner interface.
package agent

import (
	"context"
	"time"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// AgentSpawner creates and runs sub-agent query loops.
// The engine implements this interface; the Agent tool consumes it.
type AgentSpawner interface {
	// SpawnSync runs a sub-agent synchronously, blocking until it completes.
	// The sub-agent executes a full query loop with a filtered tool pool and
	// returns the collected result.
	SpawnSync(ctx context.Context, cfg *SpawnConfig) (*SpawnResult, error)
}

// SpawnConfig describes how to create and run a sub-agent.
type SpawnConfig struct {
	// Prompt is the task instruction injected as the first user message.
	Prompt string

	// AgentType controls tool filtering (sync, async, teammate, coordinator, custom).
	AgentType tool.AgentType

	// SubagentType selects the prompt profile: "general-purpose", "Explore", "Plan".
	// Empty defaults to "general-purpose".
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

	// ParentMessages holds the parent's conversation history for fork mode.
	// Only used when Fork is true. Callers must provide a deep copy.
	ParentMessages []Message

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
}

// Message is a minimal message type for fork-mode context passing.
// It mirrors the essential fields of types.Message without importing
// the full types package, keeping the agent package dependency-light.
// The engine layer converts between this and types.Message.
type Message struct {
	Role    string
	Content string
}
