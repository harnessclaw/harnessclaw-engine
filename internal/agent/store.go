// Package agent — see spawner.go for package doc.
package agent

import "context"

// AgentStore defines the persistence interface for agent definitions.
type AgentStore interface {
	Create(ctx context.Context, def *AgentDefinition) (*AgentDefinition, error)
	Get(ctx context.Context, name string) (*AgentDefinition, error)
	List(ctx context.Context, filter *AgentFilter) ([]*AgentDefinition, error)
	Update(ctx context.Context, name string, updates *AgentUpdate) (*AgentDefinition, error)
	Delete(ctx context.Context, name string) error
}

// AgentFilter holds optional filters for listing agent definitions.
type AgentFilter struct {
	AgentType *string `json:"agent_type,omitempty"`
	Source    *string `json:"source,omitempty"`
	Limit     int     `json:"limit,omitempty"`
	Offset    int     `json:"offset,omitempty"`
}

// AgentUpdate carries optional fields for updating an agent definition.
type AgentUpdate struct {
	DisplayName     *string       `json:"display_name,omitempty"`
	Description     *string       `json:"description,omitempty"`
	SystemPrompt    *string       `json:"system_prompt,omitempty"`
	Model           *string       `json:"model,omitempty"`
	Profile         *string       `json:"profile,omitempty"`
	MaxTurns        *int          `json:"max_turns,omitempty"`
	Tools           []string      `json:"tools,omitempty"`
	AllowedTools    []string      `json:"allowed_tools,omitempty"`
	DisallowedTools []string      `json:"disallowed_tools,omitempty"`
	Skills          []string      `json:"skills,omitempty"`
	AutoTeam        *bool         `json:"auto_team,omitempty"`
	SubAgents       []SubAgentDef `json:"sub_agents,omitempty"`
	Personality     *string       `json:"personality,omitempty"`
	Triggers        *string       `json:"triggers,omitempty"`
	IsTeamMember    *bool         `json:"is_team_member,omitempty"`
}
