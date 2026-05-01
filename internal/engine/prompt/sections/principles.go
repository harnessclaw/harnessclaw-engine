package sections

import (
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
)

// PrinciplesSection renders the L1 main-agent (emma) principles. Sub-agent
// profiles do NOT use this section — they inject their own role-specific
// principles via SectionOverrides (see profile.go), keeping each role's
// behavioural guidelines independently editable in prompt/texts/principles.go.
type PrinciplesSection struct{}

func NewPrinciplesSection() *PrinciplesSection {
	return &PrinciplesSection{}
}

func (s *PrinciplesSection) Name() string    { return "principles" }
func (s *PrinciplesSection) Priority() int   { return 10 }
func (s *PrinciplesSection) Cacheable() bool { return true }
func (s *PrinciplesSection) MinTokens() int  { return 100 }

func (s *PrinciplesSection) Render(_ *prompt.PromptContext, budget int) (string, error) {
	full := texts.Principles(texts.RoleEmma)
	if prompt.EstimateTokens(full) <= budget {
		return full, nil
	}
	return texts.PrinciplesCompact(texts.RoleEmma), nil
}
