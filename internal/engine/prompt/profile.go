package prompt

import (
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
			"team",        // 可派遣的 agent 名册（来自 defRegistry.TeamMembers()）
			"principles",  // 判断规则 + 交付方式（Judgment + Delivery）
			"memory",      // 用户偏好
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
		// 注：不再 override "role" —— SystemPromptOverride（动态 identity）
		// 总会赢，static role override 永远不渲染（builder.go:100）。原 ExploreRole
		// 的角色定位 + 搜索策略已搬进 explorePrinciples 顶部。
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RoleExplore),
		},
		TierWeights: map[string]float64{
			"20-29": 0.60, // give more budget to tools
		},
	}

	// PlanProfile is for Plan Mode —— 只读任务拆解，把分步实施方案写到
	// task_dir/plan.md，不执行修改。
	//
	// 加入 "tools" section：plan agent 有 AllowedTools（read / glob / grep /
	// write / edit / web_* / meta_write / submit_task_result）需要让 LLM 看到
	// 描述。旧 plan 定位是"纯讨论无工具"所以 ExcludeSections 排了 tools，
	// 新 plan 真的要调工具，必须渲染。
	PlanProfile = &AgentProfile{
		Name:        "plan",
		Description: "Plan Mode：只读任务拆解，产物是 task_dir/plan.md",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		ExcludeSections: []string{
			"memory",
		},
		// 注：不再 override "role"，原 PlanRole 已搬进 planPrinciples 顶部。
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RolePlan),
		},
	}

	// 注：旧 SchedulerProfile (L2 coordinator) 已删 ——
	// emma 的 scheduler tool 不再起 L2 LLM agent，直接派 L3 freelancer。

	// 注：旧 PlannerProfile（orchestrate 工具的内部 JSON 拆解器 sub-agent
	// profile）已删 —— orchestrate 工具本体不存在，profile 是孤儿。

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

	// ContentCreatorProfile is for the content_creator sub-agent — focused
	// on AI image/video production. Mirrors WorkerProfile layout but swaps
	// in content_creator principles so the LLM doesn't see freelancer's
	// skill-loading / L2-L3 dispatch language.
	ContentCreatorProfile = &AgentProfile{
		Name:        "content_creator",
		Description: "AI image + video production sub-agent",
		Sections: []string{
			"currentdate",
			"role",
			"principles",
			"tools",
			"env",
			"task",
		},
		SectionOverrides: map[string]string{
			"principles": principles.Principles(principles.RoleContentCreator),
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
		"emma":            EmmaProfile,
		"explore":         ExploreProfile,
		"plan":            PlanProfile,
		"worker":          WorkerProfile,
		"freelancer":      FreelancerProfile,
		"content_creator": ContentCreatorProfile,
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
// Sub-agent paths always resolve to "worker"/"explore"/"plan"
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
//	"freelancer"                  → FreelancerProfile (L3)
//	"content_creator"             → ContentCreatorProfile (L3)
//	everything else               → WorkerProfile (L3 default)
func ResolveProfileBySubagentType(subagentType string) *AgentProfile {
	switch subagentType {
	case "explore", "researcher":
		return ExploreProfile
	case "plan":
		return PlanProfile
	case "freelancer":
		return FreelancerProfile
	case "content_creator":
		return ContentCreatorProfile
	default:
		// All sub-agents use WorkerProfile by default.
		// EmmaProfile is reserved for emma (the main agent).
		return WorkerProfile
	}
}
