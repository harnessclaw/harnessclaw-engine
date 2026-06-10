// Package agentmgmt is the AgentDefinition CRUD service layer: in-memory
// AgentStore interface + SQLite-backed implementation + AgentService that
// keeps the in-memory definition.Registry in sync with the SQLite store.
// Consumed by services/api HTTP handlers and wired in cmd/server.
package agentmgmt

import (
	"context"

	"harnessclaw-go/internal/engine/agent/definition"
)

// AgentStore defines the persistence interface for agent definitions.
type AgentStore interface {
	Create(ctx context.Context, def *definition.AgentDefinition) (*definition.AgentDefinition, error)
	Get(ctx context.Context, name string) (*definition.AgentDefinition, error)
	List(ctx context.Context, filter *AgentFilter) ([]*definition.AgentDefinition, error)
	Update(ctx context.Context, name string, updates *AgentUpdate) (*definition.AgentDefinition, error)
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
	SubAgents       []definition.SubAgentDef `json:"sub_agents,omitempty"`
	Personality     *string       `json:"personality,omitempty"`
	Triggers        *string       `json:"triggers,omitempty"`
	IsTeamMember    *bool         `json:"is_team_member,omitempty"`
}
