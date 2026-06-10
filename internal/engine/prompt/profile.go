package prompt

import (
	"harnessclaw-go/internal/engine/prompt/texts"
	"harnessclaw-go/internal/engine/prompt/texts/principles"
)

// AgentProfile declares which Sections an agent type uses,
// and how to customize them.
type AgentProfile struct {
	// Name uniquely identifies this profile.
	Name string

	// Description is human-readable, for documentation and logging.
	Description string

	// Sections lists the section names to include.
	// If empty, ALL registered sections are included (default behavior).
	Sections []string

	// ExcludeSections lists sections to explicitly exclude.
	// Applied after Sections.
	ExcludeSections []string

	// SectionOverrides allows replacing a section's content with
	// a static string. Keyed by section name.
	SectionOverrides map[string]string

	// TokenBudget overrides the default system prompt budget.
	// 0 means use the computed budget from BudgetAllocator.
	TokenBudget int

	// TierWeights overrides the default tier weights for this profile.
	// Keyed by "min-max", e.g. "20-29": 0.6
	TierWeights map[string]float64
}

// Built-in profiles
var (
	// EmmaProfile is the main agent profile — emma only.
	// Emma is a routing/coordination layer: she talks to the user and dispatches
	// work to sub-agents. She does NOT use tools or skills directly.
	EmmaProfile = &AgentProfile{
		Name:        "emma",
		Description: "Emma — the main AI secretary facing the user",
		Sections: []string{
			"currentdate", // priority 90 → epilogue；放最后是为了 prompt cache 命中
			"role",        // emma 的身份和人设（Identity）
			"principles",  // 判断规则 + 交付方式（Judgment + Delivery）
			"memory",      // 用户偏好
			// "team" intentionally NOT included — emma at L1 treats L2
			// (scheduler) as a black box. The team roster is consumed
			// internally by scheduler, not by emma.
		},
	}

	// ExploreProfile is for read-only exploration.
	// Overrides "role" with explorer persona; no identity/memory/task needed.
	ExploreProfile = &AgentProfile{
		Name:        "explore",
		Description: "Read-only exploration agent with search methodology",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
		},
		SectionOverrides: map[string]string{
			"role":       texts.ExploreRole,
			"principles": principles.Principles(principles.RoleExplore),
		},
		TierWeights: map[string]float64{
			"20-29": 0.60, // give more budget to tools
		},
	}

	// PlanProfile is for planning without execution.
	// Overrides "role" with planner persona.
	PlanProfile = &AgentProfile{
		Name:        "plan",
		Description: "Planning agent that designs but does not implement",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"env",
			"task",
		},
		ExcludeSections: []string{
			"tools",
			"memory",
		},
		SectionOverrides: map[string]string{
			"role":       texts.PlanRole,
			"principles": principles.Principles(principles.RolePlan),
		},
	}

	// 注：旧 SchedulerProfile (L2 coordinator) 已删 ——
	// emma 的 scheduler tool 不再起 L2 LLM agent，直接派 L3 freelancer。

	// PlannerProfile is the internal Phase-2 task decomposer used by the
	// orchestrate tool. It is NOT part of emma's roster — emma never calls
	// it directly; orchestrate spawns it to convert a natural-language intent
	// into a structured plan JSON describing dependent sub-agent steps.
	PlannerProfile = &AgentProfile{
		Name:        "planner",
		Description: "Internal orchestrate planner — turns intent into plan JSON",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
		},
		ExcludeSections: []string{
			"tools",
			"team",
			"memory",
			"env",
			"task",
		},
		SectionOverrides: map[string]string{
			"role":       texts.PlannerRole,
			"principles": principles.Principles(principles.RolePlanner),
		},
	}

	// WorkerProfile is for emma's team members (搭档) when dispatched.
	// "role" is overridden at runtime via PromptContext.SystemPromptOverride
	// with the worker's identity (e.g., "你叫小程，emma 团队的代码开发专家").
	// No "rules" section needed — workerPrinciples already contains output rules.
	WorkerProfile = &AgentProfile{
		Name:        "worker",
		Description: "Team member dispatched by emma to execute a specific task",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RoleWorker),
		},
	}

	// FreelancerProfile is for the freelancer sub-agent — a user-skill-
	// driven L3 worker whose capability comes from runtime-loaded skills.
	// Mirrors WorkerProfile in section layout, swapping in freelancer
	// principles for self-consistency with the agent name.
	FreelancerProfile = &AgentProfile{
		Name:        "freelancer",
		Description: "Skill-driven L3 worker; capability comes from runtime-loaded user skills",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RoleFreelancer),
		},
	}

	// PlanAgentProfile is for plan_agent sub-agents that analyze goals
	// and decompose them into executable tasks via plan_update.
	PlanAgentProfile = &AgentProfile{
		Name:        "plan_agent",
		Description: "Analyzes goal and writes task breakdown to plan.json via plan_update",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		ExcludeSections: []string{"memory", "team", "skills"},
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RolePlanAgent),
		},
	}

	// PlanExecutorAgentProfile is for plan_executor_agent sub-agents that
	// read plan.json, dispatch freelancers, and update task status in real-time.
	PlanExecutorAgentProfile = &AgentProfile{
		Name:        "plan_executor_agent",
		Description: "Reads plan.json, dispatches freelancers, updates status in real-time",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		ExcludeSections: []string{"memory", "team", "skills"},
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RolePlanExecutorAgent),
		},
	}
)

// All role narratives and principles text live in the prompt/texts
// package — see SectionOverrides above for how each profile wires them
// in. Edit the text there, not here, to keep profile declarations
// focused on structure (which sections, in what order, with what budget).

// GetBuiltInProfiles returns all built-in profiles.
func GetBuiltInProfiles() map[string]*AgentProfile {
	return map[string]*AgentProfile{
		"emma":                EmmaProfile,
		"explore":             ExploreProfile,
		"plan":                PlanProfile,
		"planner":             PlannerProfile,
		"worker":              WorkerProfile,
		"freelancer":          FreelancerProfile,
		"plan_agent":          PlanAgentProfile,
		"plan_executor_agent": PlanExecutorAgentProfile,
	}
}

// DefaultMainAgentProfileName is the profile name used as a last-resort
// fallback when the caller supplies neither a default nor an explicit
// AgentContext. Production code should pass a non-empty defaultProfile to
// ResolveProfile; this constant is just a safety net.
const DefaultMainAgentProfileName = "emma"

// ResolveProfile determines the AgentProfile for the current context.
//
// The resolution order is:
//  1. agentCtx → mapped profile name (worker/explore/etc) if applicable
//  2. defaultProfile parameter (caller-supplied main-agent profile name)
//  3. DefaultMainAgentProfileName (last-resort fallback)
//
// Sub-agent paths always resolve to "worker"/"explore"/"plan"/"planner"
// regardless of the default — those names are routed via subagent_type.
func ResolveProfile(
	agentCtx *AgentContext,
	profiles map[string]*AgentProfile,
	defaultProfile string,
) *AgentProfile {
	if profiles == nil {
		profiles = GetBuiltInProfiles()
	}

	// Map agent type to profile name; main-agent paths return "" so the
	// caller-supplied default takes effect.
	profileName := agentTypeToProfile(agentCtx, defaultProfile)
	if profileName == "" {
		profileName = defaultProfile
	}
	if profileName == "" {
		profileName = DefaultMainAgentProfileName
	}

	// Look up profile
	if p, ok := profiles[profileName]; ok {
		return p
	}

	// Last-resort fallback to the documented default.
	if p, ok := profiles[DefaultMainAgentProfileName]; ok {
		return p
	}
	return WorkerProfile
}

// agentTypeToProfile maps an AgentContext to a profile name. The mainProfile
// argument is returned for main-agent paths (non-sub agents), letting callers
// inject any user-facing profile name without hardcoding "emma" here.
func agentTypeToProfile(agentCtx *AgentContext, mainProfile string) string {
	if agentCtx == nil {
		return mainProfile
	}

	switch agentCtx.AgentType {
	case "sync", "async":
		if agentCtx.IsSubAgent {
			return "worker"
		}
		return mainProfile
	case "":
		return mainProfile
	case "teammate":
		return "worker"
	case "explore":
		return "explore"
	default:
		return "worker"
	}
}

// AgentContext is a minimal subset of types.AgentContext for profile resolution.
type AgentContext struct {
	AgentType  string
	IsSubAgent bool
}

// ResolveProfileByName looks up a profile by its Name field.
// Returns nil if not found.
func ResolveProfileByName(name string) *AgentProfile {
	profiles := GetBuiltInProfiles()
	if p, ok := profiles[name]; ok {
		return p
	}
	return nil
}

// ResolveProfileBySubagentType maps a subagent_type string (from SpawnConfig)
// to the corresponding AgentProfile. This is the primary entry point for
// sub-agent profile selection.
//
// EmmaProfile is reserved for emma (the main agent) and is NEVER returned here.
//
// Mapping:
//
//	"explore" / "researcher"      → ExploreProfile (L3)
//	"plan"                        → PlanProfile (L3)
//	"planner" (legacy)            → PlannerProfile
//	"freelancer"                  → FreelancerProfile (L3)
//	"plan_agent"                  → PlanAgentProfile
//	"plan_executor_agent"         → PlanExecutorAgentProfile
//	everything else               → WorkerProfile (L3 default)
func ResolveProfileBySubagentType(subagentType string) *AgentProfile {
	switch subagentType {
	case "explore", "researcher":
		return ExploreProfile
	case "plan":
		return PlanProfile
	case "planner":
		return PlannerProfile
	case "freelancer":
		return FreelancerProfile
	case "plan_agent":
		return PlanAgentProfile
	case "plan_executor_agent":
		return PlanExecutorAgentProfile
	default:
		// All sub-agents use WorkerProfile by default.
		// EmmaProfile is reserved for emma (the main agent).
		return WorkerProfile
	}
}
