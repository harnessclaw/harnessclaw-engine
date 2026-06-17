package definition

// 本文件包含 AgentDefinition / Tier / CostTier / SubAgentDef 等"一个 agent
// 长什么样"的类型层，以及类型自带的方法（Validate / EffectiveTier 等）。
//
// Registry 容器和 CRUD 方法在同包的 registry.go。
// 内建 agent 的具体数据值在 internal/engine/agent/builtin 包。

import (
	"fmt"

	"harnessclaw-go/internal/tools"
)

// Tier classifies an agent's role in the L1 / L2 / L3 hierarchy. The engine
// uses Tier to pick a driver: coordinators route through SchedulerCoordinator,
// sub-agents run the leaf-only runSubAgentDriver.
//
// Default for an empty AgentDefinition.Tier is TierCoordinator — backward
// compatible with the pre-tier registrations.
type Tier string

const (
	// TierCoordinator can dispatch other agents and integrate their results.
	// scheduler (the L2 entry point) and Plan / Explore / general-purpose
	// fall into this tier. Coordinator spawns route through SchedulerCoordinator.
	TierCoordinator Tier = "coordinator"

	// TierSubAgent is a leaf executor — single responsibility, runs a pure
	// ReAct loop, MUST submit a structured result via SubmitTaskResult, and
	// is forbidden from calling the dispatch tools (task / scheduler) — no
	// further dispatch. freelancer is the only TierSubAgent today.
	TierSubAgent Tier = "sub_agent"
)

// CostTier hints at how expensive a single run of this agent is. Planners
// use it to balance task complexity against budget — cheap models for
// simple extraction, expensive models for multi-step analysis.
type CostTier string

const (
	CostCheap     CostTier = "cheap"
	CostMedium    CostTier = "medium"
	CostExpensive CostTier = "expensive"
)

// AgentDefinition describes a custom agent type loaded from configuration
// (e.g., .claude/agents/*.md or YAML files). It determines which tools,
// prompt, and model a spawned agent will use.
type AgentDefinition struct {
	// Name is the unique identifier for this agent type (e.g., "researcher", "test-runner").
	Name string `json:"name"`

	// DisplayName is a human-readable name shown in UI surfaces.
	DisplayName string `json:"display_name,omitempty"`

	// Description is a human-readable summary of what this agent does.
	Description string `json:"description"`

	// Profile selects the prompt profile: "full", "explore", or "plan".
	Profile string `json:"profile,omitempty"`

	// AutoTeam enables automatic team mode where sub-agents are spawned.
	AutoTeam bool `json:"auto_team,omitempty"`

	// SubAgents defines predefined sub-agents spawned automatically in team mode.
	SubAgents []SubAgentDef `json:"sub_agents,omitempty"`

	// Tools is an optional tool whitelist separate from AllowedTools.
	// When set, only these tools are available to the agent.
	Tools []string `json:"tools,omitempty"`

	// SystemPrompt is the system prompt for this agent type.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Model overrides the default LLM model for this agent type.
	Model string `json:"model,omitempty"`

	// AgentType controls tool filtering (sync, async, teammate, coordinator, custom).
	AgentType tool.AgentType `json:"agent_type"`

	// AllowedTools is the whitelist of tools this agent can use.
	// Empty means use the default filtering for the AgentType.
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// DisallowedTools is an additional blacklist applied after AgentType filtering.
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// Skills serves two purposes:
	//   1. SkillTool whitelist — when non-empty, the agent can only invoke
	//      skill commands listed here (existing behavior).
	//   2. Capability tags — Registry.FindBySkill matches against this list
	//      so a planner can pick agents by what they can do, not by name.
	// Treat each entry as both an invocation token and a capability label.
	Skills []string `json:"skills,omitempty"`

	// MaxTurns overrides the default max turns for this agent type.
	MaxTurns int `json:"max_turns,omitempty"`

	// Personality describes the agent's character traits for the team table.
	Personality string `json:"personality,omitempty"`

	// Triggers describes typical user phrases that should route to this agent.
	// Used for programmatic routing decisions, not displayed in the prompt.
	Triggers string `json:"triggers,omitempty"`

	// Source indicates where this definition was loaded from.
	Source string `json:"source,omitempty"` // e.g., file path

	// IsBuiltin marks the definition as a system built-in that cannot be deleted.
	IsBuiltin bool `json:"is_builtin,omitempty"`

	// IsTeamMember marks this agent as one of emma's team members (搭档).
	// Team members appear in emma's dynamic team table in the prompt.
	IsTeamMember bool `json:"is_team_member,omitempty"`

	// --- L3 sub-agent metadata (Tier == TierSubAgent only) -----------------
	//
	// These fields describe the sub-agent's contract to the planner: what it
	// accepts, what it produces, what it can't do, and how expensive it is.
	// They are required for TierSubAgent and ignored for TierCoordinator.

	// Tier classifies the agent. Empty defaults to TierCoordinator. Setting
	// TierSubAgent triggers strict registration (OutputSchema required) and
	// routes spawns through runSubAgentDriver.
	Tier Tier `json:"tier,omitempty"`

	// RunAsLLMAgent causes this agent to run through the subagent LLM driver
	// even when Tier is TierCoordinator. Use for coordinator-tier agents that
	// make direct LLM calls but should not be subject to the TierSubAgent
	// restrictions (OutputSchema required, dispatch tools forbidden).
	// Dispatch tool stripping is NOT applied when RunAsLLMAgent is set
	// without TierSubAgent.
	RunAsLLMAgent bool `json:"run_as_llm_agent,omitempty"`

	// OutputSchema is the JSON Schema the sub-agent's structured result must
	// satisfy. Mandatory for TierSubAgent — Register rejects sub-agents
	// without one. Drives the SubmitTaskResult tool's validation: the L3
	// MUST submit a payload matching this schema before the loop terminates.
	OutputSchema map[string]any `json:"output_schema,omitempty"`

	// InputSchema is the JSON Schema the spawn prompt is expected to carry.
	// Optional: when set, SpawnSync can validate cfg.Inputs before invoking
	// the sub-agent. Empty means "no input contract", same as today.
	InputSchema map[string]any `json:"input_schema,omitempty"`

	// Limitations lists what this sub-agent CANNOT do. Surfaced to planners
	// to prevent misuse (LLMs over-attribute capability without explicit
	// limits). Each entry is one short user-facing sentence.
	Limitations []string `json:"limitations,omitempty"`

	// ExampleTasks lists prototypical prompts this sub-agent handles well.
	// Used as few-shot guidance when a planner generates dispatches.
	ExampleTasks []string `json:"example_tasks,omitempty"`

	// CostTier indicates this sub-agent's run cost relative to peers.
	// Default (empty string) is treated as CostMedium.
	CostTier CostTier `json:"cost_tier,omitempty"`

	// TypicalLatencyMs is a rough guess at end-to-end runtime for the
	// planner's scheduling heuristics. Zero means "unknown".
	TypicalLatencyMs int `json:"typical_latency_ms,omitempty"`

	// Temperature overrides the LLM sampling temperature for this agent.
	// Pointer so "not set" is distinguishable from 0.0 (deterministic) —
	// nil means "use whatever the engine default is", value means "use
	// exactly this". writers benefit from 0.6-0.8 (more creative
	// phrasings); coders / extractors want 0.1-0.3 (deterministic, less
	// invention). Without per-agent temperature, every sub-agent inherits
	// the global default, which is wrong for at least half the workers.
	Temperature *float64 `json:"temperature,omitempty"`
}

// SubAgentDef describes a predefined sub-agent spawned automatically.
type SubAgentDef struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	AgentType tool.AgentType `json:"agent_type"`
	Profile   string         `json:"profile"`
}

// Validate checks the AgentDefinition for self-consistency. Strict rules
// for TierSubAgent are enforced here so any caller — Register, hot-reload,
// import from disk — gets the same answer.
//
// Rules:
//   - Name is required.
//   - TierSubAgent requires OutputSchema (the structured result contract).
//   - TierSubAgent forbids dispatch tools in AllowedTools (no task, no
//     scheduler). The runtime driver also strips them defensively, but
//     rejecting at registration catches misconfig early.
func (d *AgentDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("agent definition: Name is required")
	}
	if d.Tier == TierSubAgent {
		if len(d.OutputSchema) == 0 {
			return fmt.Errorf("agent %q: TierSubAgent requires OutputSchema", d.Name)
		}
		for _, t := range d.AllowedTools {
			if t == "freelance" || t == "dispatch" {
				return fmt.Errorf("agent %q: TierSubAgent cannot dispatch (tool %q forbidden)", d.Name, t)
			}
		}
	}
	return nil
}

// EffectiveTier returns Tier with the empty default resolved to TierCoordinator.
// Use this anywhere you branch on tier — never compare d.Tier to "" directly.
func (d *AgentDefinition) EffectiveTier() Tier {
	if d == nil || d.Tier == "" {
		return TierCoordinator
	}
	return d.Tier
}

// floatPtr returns a *float64 — used to set optional Temperature etc.
// without polluting the call site.
func floatPtr(v float64) *float64 { return &v }

// MaybeAugmentForSubAgent returns AllowedTools augmented with the terminal
// tools every L3 must reach (SubmitTaskResult + EscalateToPlanner). The
// augment is a no-op for nil definitions or non-SubAgent tiers, and idempotent
// when the names are already present.
//
// Why we don't bake this into the LLM prompt instead: the schema-level
// availability is what makes a tool callable; injecting "you may submit"
// into the prompt while the tool is filtered out of the pool produces a
// silent-fail loop that nudges to no avail.
func (d *AgentDefinition) MaybeAugmentForSubAgent() []string {
	if d == nil || d.EffectiveTier() != TierSubAgent || len(d.AllowedTools) == 0 {
		// Nil def, coordinator tier, or no whitelist → return as-is.
		// (Empty whitelist means "use AgentType default", which is a
		// different code path that we don't want to disturb.)
		if d == nil {
			return nil
		}
		return d.AllowedTools
	}
	// 注：escalate_to_planner 工具已删除 —— "做不到"的合法表达改走
	// meta_write({status: "failed", summary: "..."}) → submit_task_result({})。
	// 唯一兜底要求保留 submit_task_result。
	required := []string{"submit_task_result"}
	out := make([]string, 0, len(d.AllowedTools)+len(required))
	out = append(out, d.AllowedTools...)
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range required {
		if !seen[t] {
			out = append(out, t)
		}
	}
	return out
}

