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
	return fmt.Sprintf("IMPORTANT: Today's date is %s. The current year is %s. When the user says \"今年\" (this year), \"近期\" (recently), or any relative time reference, always use this date as the reference point.", ctx.EnvInfo.Date, ctx.EnvInfo.Date[:4]), nil
}
