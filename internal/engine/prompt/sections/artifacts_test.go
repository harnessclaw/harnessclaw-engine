package sections

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/prompt"
)

func TestArtifactsSectionName(t *testing.T) {
	s := NewArtifactsSection()
	if s.Name() != "artifacts" {
		t.Errorf("Name() = %q, want artifacts", s.Name())
	}
}

func TestArtifactsSectionPriority(t *testing.T) {
	s := NewArtifactsSection()
	tools := NewToolsSection()
	if s.Priority() <= tools.Priority() {
		t.Errorf("artifacts priority (%d) should be > tools priority (%d)", s.Priority(), tools.Priority())
	}
}

func TestArtifactsSectionCacheable(t *testing.T) {
	s := NewArtifactsSection()
	if !s.Cacheable() {
		t.Error("artifacts section should be cacheable (static content)")
	}
}

func TestArtifactsSectionRender(t *testing.T) {
	s := NewArtifactsSection()
	ctx := &prompt.PromptContext{}

	content, err := s.Render(ctx, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content == "" {
		t.Fatal("rendered content should not be empty")
	}

	// Key guidance must be present.
	checks := []string{
		"ArtifactGet",
		"artifact_ref",
		"art_",
		"Do NOT regenerate",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("rendered content should contain %q", check)
		}
	}
}

func TestArtifactsSectionMinTokens(t *testing.T) {
	s := NewArtifactsSection()
	if s.MinTokens() <= 0 {
		t.Error("MinTokens should be positive")
	}
}
