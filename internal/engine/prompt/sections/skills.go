package sections

import (
	"harnessclaw-go/internal/engine/prompt"
)

// SkillsSection renders the available skills listing into the system prompt.
// This replaces the per-turn <system-reminder> user message injection (Layer 3),
// making skill listing part of the structured system prompt with proper
// token budget allocation and API-level prompt caching.
type SkillsSection struct{}

func NewSkillsSection() *SkillsSection {
	return &SkillsSection{}
}

func (s *SkillsSection) Name() string    { return "skills" }
func (s *SkillsSection) Priority() int   { return 35 } // after tools(20), before task(40)
func (s *SkillsSection) Cacheable() bool { return true }
func (s *SkillsSection) MinTokens() int  { return 30 }

func (s *SkillsSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if ctx.SkillListing == "" {
		return "", nil
	}

	return "# Available Skills\n\n" +
		"Use the Skill tool to invoke these. " +
		"When a user's request matches a skill, invoke it BEFORE generating any other response.\n\n" +
		ctx.SkillListing, nil
}
