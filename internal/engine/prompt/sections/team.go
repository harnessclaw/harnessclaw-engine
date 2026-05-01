package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
)

// TeamSection renders emma's dynamic team table from PromptContext.TeamMembers.
// The static preamble/epilogue text lives in prompt/texts/team.go.
//
// Non-cacheable because the team roster can change at runtime (YAML/API).
// Priority 2: renders right after identity, before principles.
type TeamSection struct{}

func NewTeamSection() *TeamSection {
	return &TeamSection{}
}

func (s *TeamSection) Name() string    { return "team" }
func (s *TeamSection) Priority() int   { return 2 }
func (s *TeamSection) Cacheable() bool { return false }
func (s *TeamSection) MinTokens() int  { return 30 }

func (s *TeamSection) Render(ctx *prompt.PromptContext, _ int) (string, error) {
	if len(ctx.TeamMembers) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString(texts.TeamPreamble)

	b.WriteString("| 搭档 | 代号 | 精通领域 | 性格 |\n")
	b.WriteString("|------|------|---------|------|\n")
	for _, m := range ctx.TeamMembers {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			m.DisplayName, m.CodeName, m.Description, m.Personality)
	}

	b.WriteString(texts.TeamEpilogue)
	return b.String(), nil
}
