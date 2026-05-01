package agent

import (
	"sync"

	"harnessclaw-go/internal/tool"
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

	// Skills is an optional whitelist of skill names this agent can invoke.
	// When non-empty, the SkillTool will only allow invoking skills in this list,
	// and the system prompt will only list these skills.
	// When empty, all available skills are accessible (default behavior).
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

// Unregister removes an agent definition by name. Returns true if it existed.
func (r *AgentDefinitionRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.defs[name]
	delete(r.defs, name)
	return ok
}

// TeamMembers returns all definitions marked as team members, sorted by name.
func (r *AgentDefinitionRegistry) TeamMembers() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var members []*AgentDefinition
	for _, d := range r.defs {
		if d.IsTeamMember {
			members = append(members, d)
		}
	}
	// Stable sort by name for consistent prompt rendering.
	for i := 0; i < len(members); i++ {
		for j := i + 1; j < len(members); j++ {
			if members[j].Name < members[i].Name {
				members[i], members[j] = members[j], members[i]
			}
		}
	}
	return members
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
	// --- 核心团队成员（对应 emma 团队表） ---
	r.Register(&AgentDefinition{
		Name:         "writer",
		DisplayName:  "小林",
		Description:  "文案写作、邮件、报告、翻译、润色",
		Personality:  "文字功底极好，别人写三段才能说清的事他一段就能写到位。安静内敛，但交出来的东西总是打磨得很细",
		AgentType:    tool.AgentTypeSync,
		Profile:      "worker",
		IsTeamMember: true,
	})
	r.Register(&AgentDefinition{
		Name:         "researcher",
		DisplayName:  "小瑞",
		Description:  "信息搜索、行业调研、资料整理、摘要",
		Personality:  "信息嗅觉很灵，能从一堆噪音里捞出最关键的几条。做事严谨，给出的信息都会标注来源和可信度",
		AgentType:    tool.AgentTypeSync,
		Profile:      "explore",
		IsTeamMember: true,
	})
	r.Register(&AgentDefinition{
		Name:         "analyst",
		DisplayName:  "小数",
		Description:  "数据分析、表格处理、图表生成",
		Personality:  "对数字极其敏感，一眼能看出数据里藏着什么故事。擅长把复杂数据变成一看就懂的图表",
		AgentType:    tool.AgentTypeSync,
		Profile:      "worker",
		IsTeamMember: true,
	})
	r.Register(&AgentDefinition{
		Name:         "developer",
		DisplayName:  "小程",
		Description:  "代码编写、技术问题、工具开发",
		Personality:  "写代码又快又稳，解释技术问题不会满嘴术语，用你听得懂的话说",
		AgentType:    tool.AgentTypeSync,
		Profile:      "worker",
		IsTeamMember: true,
	})
	r.Register(&AgentDefinition{
		Name:         "lifestyle",
		DisplayName:  "小悦",
		Description:  "出行规划、餐饮推荐、购物比价、生活事务",
		Personality:  "品味好，对吃喝玩乐门儿清，推荐的地方基本不踩雷。做出行规划考虑得很周全",
		AgentType:    tool.AgentTypeSync,
		Profile:      "worker",
		IsTeamMember: true,
	})
	r.Register(&AgentDefinition{
		Name:         "scheduler",
		DisplayName:  "小时",
		Description:  "日程管理、会议安排、时间规划、提醒",
		Personality:  "时间管理能力极强，排日程像下棋，总能找到最优解。不会把会议排得密不透风，会留出缓冲",
		AgentType:    tool.AgentTypeSync,
		Profile:      "worker",
		IsTeamMember: true,
	})

	// --- 系统级 agent（不在用户可见的搭档表中） ---
	r.Register(&AgentDefinition{
		Name:        "specialists",
		DisplayName: "Specialists",
		Description: "L2 调度统筹者：拆解任务、派 L3 sub-agent、整合产出、检查质量",
		AgentType:   tool.AgentTypeSync,
		Profile:     "specialists",
		// Specialists needs an explicit tool whitelist so it can use the
		// Task tool to dispatch L3 sub-agents. The tool filter pipeline
		// in subagent.go treats AllowedTools as authoritative — it
		// bypasses the AgentType blacklist (which would otherwise block
		// Task for sync sub-agents).
		AllowedTools: []string{"Task", "WebSearch", "TavilySearch"},
	})
	r.Register(&AgentDefinition{
		Name:        "general-purpose",
		DisplayName: "通用执行者",
		Description: "通用 agent，处理不属于特定搭档领域的复杂多步骤任务",
		AgentType:   tool.AgentTypeSync,
		Profile:     "worker",
	})
	r.Register(&AgentDefinition{
		Name:        "Explore",
		DisplayName: "探索者",
		Description: "快速只读探索 agent，搜索代码库、定位文件",
		AgentType:   tool.AgentTypeSync,
		Profile:     "explore",
	})
	r.Register(&AgentDefinition{
		Name:        "Plan",
		DisplayName: "规划者",
		Description: "方案设计 agent，需求分析、架构设计、方案对比",
		AgentType:   tool.AgentTypeSync,
		Profile:     "plan",
	})
	// Coordinator definition removed — orchestration logic moved to application code (Phase 2).
}
