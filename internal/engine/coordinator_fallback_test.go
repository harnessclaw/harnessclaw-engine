package engine

import (
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestFallbackChain_PreservesPartialArtifacts(t *testing.T) {
	f := NewFallbackChain(nil)
	out := f.Aggregate(FallbackInput{
		Goal:   "research and write",
		Reason: "judge_goal failed",
		Results: []*StepResult{
			{StepID: "s1", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "art_search"}}},
			{StepID: "s2", Status: "failed"},
		},
	})
	if len(out.Artifacts) != 1 || out.Artifacts[0].ArtifactID != "art_search" {
		t.Errorf("partial artifact lost; got %v", out.Artifacts)
	}
	if !strings.Contains(out.Summary, "已完成 1 步") {
		t.Errorf("summary should report 1 success; got %q", out.Summary)
	}
	if !strings.Contains(out.Summary, "未完成 1 步") {
		t.Errorf("summary should report 1 failure; got %q", out.Summary)
	}
}

func TestFallbackChain_DedupsArtifacts(t *testing.T) {
	// Two steps that reference the same artifact (e.g. step B reads what
	// step A wrote, and Plan tracking surfaces it twice). The chain
	// should not duplicate.
	f := NewFallbackChain(nil)
	a := types.ArtifactRef{ArtifactID: "art_dup"}
	out := f.Aggregate(FallbackInput{
		Reason: "x",
		Results: []*StepResult{
			{StepID: "s1", Status: "success", Artifacts: []types.ArtifactRef{a}},
			{StepID: "s2", Status: "success", Artifacts: []types.ArtifactRef{a}},
		},
	})
	if len(out.Artifacts) != 1 {
		t.Errorf("dedup failed; got %d artifacts", len(out.Artifacts))
	}
}

func TestFallbackChain_BudgetExceededSurfaceInSummary(t *testing.T) {
	f := NewFallbackChain(nil)
	out := f.Aggregate(FallbackInput{
		Goal:   "expensive thing",
		Reason: "budget exhausted",
		Results: []*StepResult{
			{StepID: "s1", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "a"}}},
		},
		Budget: BudgetSnapshot{
			TokensUsed: 250000,
			Exceeded:   true,
			ExceededWhy: "token budget exhausted: used 250000 > 200000",
		},
	})
	if !strings.Contains(out.Summary, "预算耗尽") {
		t.Errorf("budget reason missing from summary: %q", out.Summary)
	}
	found := false
	for _, na := range out.NeedsAttention {
		if strings.Contains(na, "token budget") {
			found = true
		}
	}
	if !found {
		t.Errorf("NeedsAttention should mention budget exhaustion; got %v", out.NeedsAttention)
	}
}

func TestFallbackChain_NeedsAttentionListsFailedSteps(t *testing.T) {
	f := NewFallbackChain(nil)
	out := f.Aggregate(FallbackInput{
		Reason: "x",
		Results: []*StepResult{
			{StepID: "s1", Status: "failed"},
			{StepID: "s2", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "a"}}},
			{StepID: "s3", Status: "skipped"},
		},
	})
	if len(out.NeedsAttention) != 2 {
		t.Errorf("expected 2 entries (s1 failed, s3 skipped); got %d: %v",
			len(out.NeedsAttention), out.NeedsAttention)
	}
}

func TestFallbackChain_ReproducibleOrder(t *testing.T) {
	// Two runs with the same input must produce identical summaries —
	// pin via stable sort of failures and dedup-by-insertion-order for
	// artifacts. Snapshot tests rely on this.
	f := NewFallbackChain(nil)
	in := FallbackInput{
		Goal:   "test",
		Reason: "x",
		Results: []*StepResult{
			{StepID: "s_b", Status: "failed"},
			{StepID: "s_a", Status: "failed"},
			{StepID: "s_c", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "art"}}},
		},
	}
	a := f.Aggregate(in)
	b := f.Aggregate(in)
	if a.Summary != b.Summary {
		t.Errorf("aggregate not deterministic:\nrun A: %q\nrun B: %q", a.Summary, b.Summary)
	}
}
