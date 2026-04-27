package sections

import (
	"harnessclaw-go/internal/engine/prompt"
)

// RoleSection renders the agent's identity (persona).
// For the main agent (emma), it renders identity content directly.
// Sub-agent profiles override "role" via SectionOverrides to inject
// their own persona — the override bypasses Render() entirely.
type RoleSection struct {
	identity *IdentitySection
}

func NewRoleSection() *RoleSection {
	return &RoleSection{
		identity: NewIdentitySection(),
	}
}

func (s *RoleSection) Name() string     { return "role" }
func (s *RoleSection) Priority() int    { return 1 }
func (s *RoleSection) Cacheable() bool  { return true } // static persona (team table moved to TeamSection)
func (s *RoleSection) MinTokens() int   { return 50 }

func (s *RoleSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if ctx.SystemPromptOverride != "" {
		return ctx.SystemPromptOverride, nil
	}
	return s.identity.Render(ctx, budget)
}
