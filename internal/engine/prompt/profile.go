package prompt

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
	// FullProfile includes all sections - default for general use
	FullProfile = &AgentProfile{
		Name:        "full",
		Description: "Full-capability assistant with all sections",
		Sections:    []string{}, // empty = all
	}

	// ExploreProfile is for read-only exploration
	ExploreProfile = &AgentProfile{
		Name:        "explore",
		Description: "Read-only exploration agent",
		Sections: []string{
			"role",
			"principles",
			"tools",
			"env",
		},
		SectionOverrides: map[string]string{
			"role": `You are Kiro, a fast read-only exploration agent.
You can search, read, and analyze files but CANNOT modify any files.
Your role is to help users understand codebases and find information quickly.`,
		},
		TierWeights: map[string]float64{
			"20-29": 0.60, // give more budget to tools
		},
	}

	// PlanProfile is for planning without execution
	PlanProfile = &AgentProfile{
		Name:        "plan",
		Description: "Planning agent that designs but does not implement",
		Sections: []string{
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
			"role": `You are Kiro, a planning and design agent.
Your role is to analyze requirements, design solutions, and create implementation plans.
You have access to read tools for exploration but CANNOT edit files or run commands.`,
		},
	}
)

// GetBuiltInProfiles returns all built-in profiles.
func GetBuiltInProfiles() map[string]*AgentProfile {
	return map[string]*AgentProfile{
		"full":    FullProfile,
		"explore": ExploreProfile,
		"plan":    PlanProfile,
	}
}

// ResolveProfile determines the AgentProfile for the current context.
func ResolveProfile(
	agentCtx *AgentContext,
	profiles map[string]*AgentProfile,
	defaultProfile string,
) *AgentProfile {
	if profiles == nil {
		profiles = GetBuiltInProfiles()
	}

	// Map agent type to profile name
	profileName := agentTypeToProfile(agentCtx)
	if profileName == "" {
		profileName = defaultProfile
	}
	if profileName == "" {
		profileName = "full"
	}

	// Look up profile
	if p, ok := profiles[profileName]; ok {
		return p
	}

	// Fallback to full
	return FullProfile
}

func agentTypeToProfile(agentCtx *AgentContext) string {
	if agentCtx == nil {
		return "full"
	}

	switch agentCtx.AgentType {
	case "sync", "async", "":
		return "full"
	case "teammate":
		return "full"
	case "coordinator":
		return "plan"
	case "explore":
		return "explore"
	default:
		return ""
	}
}

// AgentContext is a minimal subset of types.AgentContext for profile resolution.
type AgentContext struct {
	AgentType string
	IsSubAgent bool
}
