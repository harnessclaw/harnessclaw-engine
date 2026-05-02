package sections

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/prompt"
)

func TestArtifactsSection_Identity(t *testing.T) {
	s := NewArtifactsSection()
	if s.Name() != "artifacts" {
		t.Errorf("Name = %q, want %q", s.Name(), "artifacts")
	}
	// Sits between tools(20) and env(30) so workers read it next to the
	// tool roster while "what tools exist" is fresh.
	if s.Priority() <= 20 || s.Priority() >= 30 {
		t.Errorf("Priority = %d, want 20 < p < 30 (between tools and env)", s.Priority())
	}
	if !s.Cacheable() {
		t.Error("section is profile-static; must be cacheable to keep prompt prefix stable")
	}
}

func TestArtifactsSection_RenderCarriesDesignRules(t *testing.T) {
	// Each phrase below traces back to a chapter in the artifact design doc.
	// If any drifts, the prompt has lost a load-bearing rule and the LLM
	// will regress to one of the documented anti-patterns.
	out, err := NewArtifactsSection().Render(&prompt.PromptContext{}, 1000)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	cases := []struct {
		need   string
		reason string // why this phrase matters
	}{
		{"按引用传递", "ch.2 mental model — pasting-by-value is the failure mode we're avoiding"},
		{"ArtifactWrite", "ch.5 — must mention the write tool by exact name"},
		{"ArtifactRead", "ch.5 — must mention the read tool by exact name"},
		{"万能背包", "ch.12 anti-pattern — workers must NOT save everything"},
		{"schema", "ch.4/12 — structured types without schema break downstream"},
		{"metadata", "ch.5 — three-mode read (metadata is the lightest tier)"},
		{"preview", "ch.5 — preview is the default scan tier"},
		{"full", "ch.5 — full is the heaviest tier, must be opt-in"},
		{"绝不能自己编", "ch.12 — never let the LLM hallucinate IDs"},
		{"parent_artifact_id", "ch.8 — modifications produce versions, not in-place overwrites"},
	}
	for _, c := range cases {
		if !strings.Contains(out, c.need) {
			t.Errorf("artifacts guidance missing %q (%s)\nfull text:\n%s", c.need, c.reason, out)
		}
	}
}
