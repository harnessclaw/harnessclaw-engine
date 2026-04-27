package sections

import (
	"fmt"

	"harnessclaw-go/internal/engine/prompt"
)

// CurrentDateSection renders the current date at the very top of the system prompt.
// Priority 0 ensures it renders before all other sections including role.
// This is critical for LLMs to correctly interpret relative time references
// like "this year", "today", "recently", etc.
type CurrentDateSection struct{}

func NewCurrentDateSection() *CurrentDateSection {
	return &CurrentDateSection{}
}

func (s *CurrentDateSection) Name() string     { return "currentdate" }
func (s *CurrentDateSection) Priority() int    { return 1 }
func (s *CurrentDateSection) Cacheable() bool  { return true }
func (s *CurrentDateSection) MinTokens() int   { return 5 }

func (s *CurrentDateSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if ctx.EnvInfo.Date == "" {
		return "", nil
	}
	return fmt.Sprintf("重要：今天是 %s，当前年份是 %s 年。用户提到「今年」「近期」「最近」等相对时间时，以此日期为基准。", ctx.EnvInfo.Date, ctx.EnvInfo.Date[:4]), nil
}
