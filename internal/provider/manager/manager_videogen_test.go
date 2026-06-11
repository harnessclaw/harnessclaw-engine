package manager

import "testing"

func TestAgentPatchVideoGeneration(t *testing.T) {
	t.Parallel()
	v := "doubao:seedance-lite-i2v"
	p := AgentPatch{VideoGeneration: &v}
	if p.IsEmpty() {
		t.Fatal("patch with VideoGeneration should not be empty")
	}
}

func TestUpdateAgent_VideoGenerationRoundTrip(t *testing.T) {
	t.Parallel()
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	v := "doubao:seedance-lite-i2v"
	if err := m.UpdateAgent(AgentPatch{VideoGeneration: &v}); err != nil {
		t.Fatalf("UpdateAgent(VideoGeneration) err = %v", err)
	}
	if got := m.CurrentAgent().VideoGeneration; got != v {
		t.Fatalf("CurrentAgent().VideoGeneration = %q, want %q", got, v)
	}
}
