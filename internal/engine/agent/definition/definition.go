package definition

import (
	"fmt"
	"sort"
	"sync"

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
	// make direct LLM calls (e.g. plan_agent, plan_executor_agent) but should
	// not be subject to the TierSubAgent restrictions (OutputSchema required,
	// dispatch tools forbidden). Dispatch tool stripping is NOT applied when
	// RunAsLLMAgent is set without TierSubAgent.
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
	required := []string{"submit_task_result", "escalate_to_planner"}
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

// Registry holds all loaded agent definitions.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*AgentDefinition
}

// NewRegistry creates a new registry.
func NewRegistry() *Registry {
	return &Registry{
		defs: make(map[string]*AgentDefinition),
	}
}

// Register adds an agent definition after validation. Returns an error when
// the definition fails Validate (e.g., TierSubAgent with no OutputSchema).
// Overwrites if the name already exists — last write wins, same as before.
//
// Callers that want to register a known-good definition without checking the
// error can use MustRegister, which panics on validation failure.
func (r *Registry) Register(def *AgentDefinition) error {
	if def == nil {
		return fmt.Errorf("agent definition: nil")
	}
	if err := def.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[def.Name] = def
	return nil
}

// MustRegister registers a definition and panics on validation failure.
// Use this for built-in registrations where a validation error is a programmer
// bug, not runtime input.
func (r *Registry) MustRegister(def *AgentDefinition) {
	if err := r.Register(def); err != nil {
		panic(fmt.Sprintf("agent.MustRegister: %v", err))
	}
}

// Get returns a definition by name, or nil.
func (r *Registry) Get(name string) *AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defs[name]
}

// All returns all registered definitions.
func (r *Registry) All() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		result = append(result, d)
	}
	return result
}

// Unregister removes an agent definition by name. Returns true if it existed.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.defs[name]
	delete(r.defs, name)
	return ok
}

// TeamMembers returns all definitions marked as team members, sorted by name.
func (r *Registry) TeamMembers() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var members []*AgentDefinition
	for _, d := range r.defs {
		if d.IsTeamMember {
			members = append(members, d)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members
}

// Names returns all registered agent definition names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	return names
}

// FindBySkill returns every registered definition whose Skills list contains
// skill. The match is exact, case-sensitive — capability tags are an
// internal vocabulary, not free-form text.
//
// Used by L2 planners to enumerate "who can do X" without knowing names.
// Result order is by definition name (stable, for deterministic prompts).
func (r *Registry) FindBySkill(skill string) []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matches []*AgentDefinition
	for _, d := range r.defs {
		for _, s := range d.Skills {
			if s == skill {
				matches = append(matches, d)
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	return matches
}

// PlannerListing is the structured snapshot a planner consumes when picking
// which sub-agent to dispatch. Strips fields the planner doesn't need
// (Source, IsBuiltin, etc.) and surfaces the contract in one place.
type PlannerListing struct {
	Name             string         `json:"name"`
	DisplayName      string         `json:"display_name,omitempty"`
	Description      string         `json:"description"`
	Skills           []string       `json:"skills,omitempty"`
	Limitations      []string       `json:"limitations,omitempty"`
	ExampleTasks     []string       `json:"example_tasks,omitempty"`
	CostTier         CostTier       `json:"cost_tier,omitempty"`
	TypicalLatencyMs int            `json:"typical_latency_ms,omitempty"`
	OutputSchema     map[string]any `json:"output_schema,omitempty"`
}

// ListForPlanner returns a planner-shaped view of every TierSubAgent in the
// registry, sorted by name. Coordinators are excluded — a planner picks
// among leaves, not among other planners.
func (r *Registry) ListForPlanner() []PlannerListing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []PlannerListing
	for _, d := range r.defs {
		if d.EffectiveTier() != TierSubAgent {
			continue
		}
		out = append(out, PlannerListing{
			Name:             d.Name,
			DisplayName:      d.DisplayName,
			Description:      d.Description,
			Skills:           d.Skills,
			Limitations:      d.Limitations,
			ExampleTasks:     d.ExampleTasks,
			CostTier:         d.CostTier,
			TypicalLatencyMs: d.TypicalLatencyMs,
			OutputSchema:     d.OutputSchema,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RegisterBuiltins adds the standard built-in agent definitions. All
// built-ins use MustRegister — a validation failure here is a programmer
// bug in this file, not runtime input.
//
// Tier policy:
//   - Built-in workers are migrating to TierSubAgent one at a time. As
//     each gets a hand-crafted OutputSchema / Limitations / Skills /
//     CostTier, it flips.
//   - System agents (scheduler / general-purpose / Plan / Explore) are
//     coordinators by design — they may dispatch and integrate.
func (r *Registry) RegisterBuiltins() {
	// 注：旧 "scheduler" L2 coordinator AgentDefinition 已删 ——
	// emma 的 scheduler tool 不再起 L2 LLM agent，直接派 L3 freelancer。
	r.MustRegister(&AgentDefinition{
		Name:        "plan",
		DisplayName: "规划者",
		Description: "方案设计 agent，需求分析、架构设计、方案对比",
		AgentType:   tool.AgentTypeSync,
		Profile:     "plan",
	})
	// --- Freelancer ---------------------------------------------------
	// User-skill-driven L3. Capability comes from runtime-loaded skills;
	// AllowedTools are statically declared by the project — skill files
	// CANNOT extend this list. High-risk tools (Bash / FileWrite) remain
	// gated by PermissionManager regardless of skill content.
	r.MustRegister(&AgentDefinition{
		Name:         "freelancer",
		DisplayName:  "外援",
		Description:  "通用执行体，能力由装载的 user skill 决定（来自本地 skills/ 目录）",
		Tier:         TierSubAgent,
		Profile:      "freelancer",
		AgentType:    tool.AgentTypeSync,
		IsTeamMember: false,
		AllowedTools: []string{
			// Generic file + shell, gated by PermissionManager.
			// Note: tool registry keys are the literal Tool.Name() values
			// — Read / Edit / Write — not FileRead / FileEdit / FileWrite.
			// The earlier "FileRead" etc. strings silently failed to match
			// any registered tool, so freelancer was effectively running
			// Bash-only for file ops. Fixed to the actual registered names.
			"read", "edit", "write", "glob", "bash",
			// Web + artifact
			"web_fetch", "web_search", "tavily_search",
			"meta_write",
			// Skill self-management — the four tools introduced by this design
			"search_skill", "load_skill", "unload_skill", "list_loaded_skills",
			// Terminal tools (also auto-augmented by MaybeAugmentForSubAgent;
			// listed here explicitly for legibility)
			"submit_task_result", "escalate_to_planner",
		},
		// InputSchema validates the structured cfg.Inputs only. The actual
		// task description flows through the task tool's `prompt` →
		// cfg.Prompt (NOT cfg.Inputs), so we don't list/require `task`
		// here — doing so would fail validation whenever cfg.Inputs is
		// non-empty (e.g. when scheduler includes candidate_skills) since
		// the task text never lands in cfg.Inputs. The only thing this
		// schema needs to enforce is the shape of candidate_skills.
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"candidate_skills": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"maxItems":    3,
					"description": "L2 预选的 skill 名字列表。spawn 时框架会预加载它们的 SKILL.md body 到 freelancer 上下文。",
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"artifact_role", "skills_used"},
			"properties": map[string]any{
				"artifact_role": map[string]any{
					"type":        "string",
					"description": "本次产出的角色标签（由 skill 上下文决定具体语义）。",
				},
				"skills_used": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "实际影响本次产出的 skill 名字（不一定等于所有加载的 skill）。",
				},
			},
		},
		CostTier: CostMedium,
		Limitations: []string{
			"能力完全取决于装载的 skill",
			"上下文中并存 skill body 数量上限 3（含 L2 预分配）",
			"高危操作（Bash / FileWrite 等）每次需用户授权",
			"skill 行为偏离不应硬走，应 EscalateToPlanner",
		},
	})
	// --- Plan Mode Agents -----------------------------------------------
	r.MustRegister(&AgentDefinition{
		Name:          "plan_agent",
		DisplayName:   "规划员",
		Description:   "分析 goal，生成任务分解写入 plan.json，不执行任务",
		Profile:       "plan_agent",
		AgentType:     tool.AgentTypeSync,
		RunAsLLMAgent: true,
		AllowedTools: []string{
			"plan_update",
			"read",
			"meta_write",
			"submit_task_result",
		},
	})
	r.MustRegister(&AgentDefinition{
		Name:          "plan_executor_agent",
		DisplayName:   "执行协调员",
		Description:   "按 plan.json 任务清单调度 freelancer 执行，实时更新任务状态",
		Profile:       "plan_executor_agent",
		AgentType:     tool.AgentTypeSync,
		RunAsLLMAgent: true,
		AllowedTools: []string{
			"plan_read",
			"plan_update",
			"freelance",
			"meta_write",
			"submit_task_result",
		},
	})
	// Coordinator definition removed — orchestration logic moved to application code (Phase 2).
}
