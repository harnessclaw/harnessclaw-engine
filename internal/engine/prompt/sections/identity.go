package sections

import (
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
)

// IdentitySection defines emma's static persona — who she is, personality,
// communication style with scenario-specific examples. The actual text
// lives in prompt/texts/identity.go so static prompt content stays in
// one place across the codebase.
//
// Cacheable because the content never changes at runtime.
type IdentitySection struct{}

func NewIdentitySection() *IdentitySection {
	return &IdentitySection{}
}

func (s *IdentitySection) Name() string    { return "identity" }
func (s *IdentitySection) Priority() int   { return 1 }
func (s *IdentitySection) Cacheable() bool { return true }
func (s *IdentitySection) MinTokens() int  { return 50 }

func (s *IdentitySection) Render(_ *prompt.PromptContext, _ int) (string, error) {
	return texts.EmmaIdentity, nil
}
