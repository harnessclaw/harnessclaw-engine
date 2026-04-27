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

	return "# 可用技能\n\n" +
		"使用 Skill 工具调用以下技能。" +
		"当用户请求匹配某项技能时，先调用技能再生成其他回复。\n\n" +
		ctx.SkillListing, nil
}
