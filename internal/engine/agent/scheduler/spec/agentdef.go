package spec

// AgentDef declares an agent's profile: who it is, what it can do.
// Phase 1 keeps fields minimal; expand per migration of internal/agent/definition.go.
type AgentDef struct {
	Name         string   `json:"name"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Model        string   `json:"model,omitempty"`
}
