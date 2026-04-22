package agent

import (
	"sync"

	"harnessclaw-go/internal/tool"
)

// DefaultCoordinatorSystemPrompt is the system prompt for the coordinator agent.
// It is set by the engine's prompt package during initialization to avoid
// a circular dependency (agent → prompt). Until set, a basic fallback is used.
var DefaultCoordinatorSystemPrompt string

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

	// MaxTurns overrides the default max turns for this agent type.
	MaxTurns int `json:"max_turns,omitempty"`

	// Source indicates where this definition was loaded from.
	Source string `json:"source,omitempty"` // e.g., file path
}

// SubAgentDef describes a predefined sub-agent spawned automatically.
type SubAgentDef struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	AgentType tool.AgentType `json:"agent_type"`
	Profile   string         `json:"profile"`
}

// AgentDefinitionRegistry holds all loaded agent definitions.
type AgentDefinitionRegistry struct {
	mu   sync.RWMutex
	defs map[string]*AgentDefinition
}

// NewAgentDefinitionRegistry creates a new registry.
func NewAgentDefinitionRegistry() *AgentDefinitionRegistry {
	return &AgentDefinitionRegistry{
		defs: make(map[string]*AgentDefinition),
	}
}

// Register adds an agent definition. Overwrites if name already exists.
func (r *AgentDefinitionRegistry) Register(def *AgentDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[def.Name] = def
}

// Get returns a definition by name, or nil.
func (r *AgentDefinitionRegistry) Get(name string) *AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defs[name]
}

// All returns all registered definitions.
func (r *AgentDefinitionRegistry) All() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		result = append(result, d)
	}
	return result
}

// Names returns all registered agent definition names.
func (r *AgentDefinitionRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	return names
}

// RegisterBuiltins adds the standard built-in agent definitions.
func (r *AgentDefinitionRegistry) RegisterBuiltins() {
	r.Register(&AgentDefinition{
		Name:        "general-purpose",
		DisplayName: "General Purpose",
		Description: "General-purpose agent for complex, multi-step tasks",
		AgentType:   tool.AgentTypeSync,
		Profile:     "full",
	})
	r.Register(&AgentDefinition{
		Name:        "Explore",
		DisplayName: "Explorer",
		Description: "Fast agent specialized for exploring codebases",
		AgentType:   tool.AgentTypeSync,
		Profile:     "explore",
	})
	r.Register(&AgentDefinition{
		Name:        "Plan",
		DisplayName: "Planner",
		Description: "Software architect agent for designing implementation plans",
		AgentType:   tool.AgentTypeSync,
		Profile:     "plan",
	})
	r.Register(&AgentDefinition{
		Name:         "coordinator",
		DisplayName:  "Coordinator",
		Description:  "Team leader that delegates work to teammates and synthesizes results",
		SystemPrompt: DefaultCoordinatorSystemPrompt,
		AgentType:    tool.AgentTypeCoordinator,
		AllowedTools: []string{"Agent", "TaskStop", "SendMessage", "SyntheticOutput"},
		MaxTurns:     50,
		Profile:      "plan",
	})
	r.Register(&AgentDefinition{
		Name:        "code-reviewer",
		Description: "Code review and security audit agent",
		AgentType:   tool.AgentTypeSync,
		Profile:     "explore",
	})
	r.Register(&AgentDefinition{
		Name:        "translator",
		Description: "Multi-language translation agent",
		AgentType:   tool.AgentTypeCoordinator,
		Profile:     "plan",
		AutoTeam:    true,
	})
	r.Register(&AgentDefinition{
		Name:        "data-analyst",
		Description: "Data analysis and reporting agent",
		AgentType:   tool.AgentTypeCoordinator,
		Profile:     "plan",
		AutoTeam:    true,
	})
	r.Register(&AgentDefinition{
		Name:        "content-writer",
		Description: "Batch content production agent",
		AgentType:   tool.AgentTypeCoordinator,
		Profile:     "plan",
		AutoTeam:    true,
	})
	r.Register(&AgentDefinition{
		Name:        "doc-reviewer",
		Description: "Multi-dimensional document review agent",
		AgentType:   tool.AgentTypeCoordinator,
		Profile:     "plan",
		AutoTeam:    true,
	})
}
