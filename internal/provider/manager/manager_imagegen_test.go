package manager

import "testing"

func TestAgentPatchImageGeneration(t *testing.T) {
	t.Parallel()
	v := "openai:gpt-image"
	p := AgentPatch{ImageGeneration: &v}
	if p.IsEmpty() {
		t.Fatal("patch with ImageGeneration should not be empty")
	}
}

// TestUpdateAgent_ImageGenerationNotChainValidated confirms that
// agent.image_generation is accepted even when the ref does NOT
// exist in cfg.LLM — it resolves against cfg.ImageGen instead, so
// the LLM chain validator must NOT be applied (same as video_generation).
func TestUpdateAgent_ImageGenerationNotChainValidated(t *testing.T) {
	t.Parallel()
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	// "openai:gpt-image" is intentionally NOT present in baseCfg()'s
	// LLM providers — it would only live under cfg.ImageGen. UpdateAgent
	// must succeed without validating it against the LLM chain.
	ref := "openai:gpt-image"
	if err := m.UpdateAgent(AgentPatch{ImageGeneration: &ref}); err != nil {
		t.Fatalf("UpdateAgent(ImageGeneration=%q) err = %v; want nil (cfg.ImageGen ref must not be chain-validated)", ref, err)
	}
	if got := m.CurrentAgent().ImageGeneration; got != ref {
		t.Fatalf("CurrentAgent().ImageGeneration = %q, want %q", got, ref)
	}
}

func TestUpdateAgent_ImageGenerationRoundTrip(t *testing.T) {
	t.Parallel()
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	v := "openai:gpt-image"
	if err := m.UpdateAgent(AgentPatch{ImageGeneration: &v}); err != nil {
		t.Fatalf("UpdateAgent(ImageGeneration) err = %v", err)
	}
	if got := m.CurrentAgent().ImageGeneration; got != v {
		t.Fatalf("CurrentAgent().ImageGeneration = %q, want %q", got, v)
	}
}

func TestAgentSnapshotIncludesImageGeneration(t *testing.T) {
	t.Parallel()
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	v := "openai:gpt-image"
	if err := m.UpdateAgent(AgentPatch{ImageGeneration: &v}); err != nil {
		t.Fatalf("UpdateAgent(ImageGeneration) err = %v", err)
	}
	if got := m.AgentSnapshot().ImageGeneration; got != v {
		t.Fatalf("AgentSnapshot().ImageGeneration = %q, want %q", got, v)
	}
}
