package sessionstats

import (
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestTracker_RecordLLMCall_AccumulatesTopLevel(t *testing.T) {
	tr := NewTracker("sess_abc")

	tr.RecordLLMCall("claude-opus-4-7", "", &types.Usage{
		InputTokens: 100, OutputTokens: 50, CacheRead: 20, CacheWrite: 5, ThinkingTokens: 8,
	}, 500)
	tr.RecordLLMCall("claude-opus-4-7", "", &types.Usage{
		InputTokens: 200, OutputTokens: 30,
	}, 1500)

	s := tr.Snapshot()

	if s.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", s.InputTokens)
	}
	if s.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", s.OutputTokens)
	}
	if s.CacheReadTokens != 20 || s.CacheWriteTokens != 5 {
		t.Errorf("cache: read=%d write=%d", s.CacheReadTokens, s.CacheWriteTokens)
	}
	if s.ThinkingTokens != 8 {
		t.Errorf("ThinkingTokens = %d, want 8", s.ThinkingTokens)
	}
	if s.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2", s.LLMCalls)
	}
	if s.LatencyMsTotal != 2000 {
		t.Errorf("LatencyMsTotal = %d, want 2000", s.LatencyMsTotal)
	}
	if s.LatencyMsAvg != 1000 {
		t.Errorf("LatencyMsAvg = %d, want 1000", s.LatencyMsAvg)
	}
}

func TestTracker_RecordLLMCall_AggregatesPerModel(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RecordLLMCall("opus", "", &types.Usage{InputTokens: 100, OutputTokens: 20}, 100)
	tr.RecordLLMCall("sonnet", "", &types.Usage{InputTokens: 50, OutputTokens: 10}, 200)
	tr.RecordLLMCall("opus", "", &types.Usage{InputTokens: 30, OutputTokens: 5}, 300)

	s := tr.Snapshot()
	if len(s.PerModel) != 2 {
		t.Fatalf("PerModel length = %d, want 2", len(s.PerModel))
	}
	var opus, sonnet *types.ModelStats
	for i := range s.PerModel {
		switch s.PerModel[i].Model {
		case "opus":
			opus = &s.PerModel[i]
		case "sonnet":
			sonnet = &s.PerModel[i]
		}
	}
	if opus == nil || opus.InputTokens != 130 || opus.LLMCalls != 2 {
		t.Errorf("opus stats wrong: %+v", opus)
	}
	if sonnet == nil || sonnet.InputTokens != 50 || sonnet.LLMCalls != 1 {
		t.Errorf("sonnet stats wrong: %+v", sonnet)
	}
}

func TestTracker_RecordLLMCall_NilUsageIsSafe(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RecordLLMCall("opus", "", nil, 100)
	s := tr.Snapshot()
	if s.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1", s.LLMCalls)
	}
	if s.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", s.InputTokens)
	}
}

func TestTracker_CacheHitRate_AndThinkingShare(t *testing.T) {
	tr := NewTracker("sess_abc")
	// total input = 80, cache_read = 20 -> hit_rate = 20 / (20 + 80) = 0.2
	// total output = 50, thinking = 10 -> share = 10 / 50 = 0.2
	tr.RecordLLMCall("opus", "", &types.Usage{
		InputTokens: 80, OutputTokens: 50, CacheRead: 20, ThinkingTokens: 10,
	}, 100)
	s := tr.Snapshot()
	if got := s.CacheHitRate; got < 0.19 || got > 0.21 {
		t.Errorf("CacheHitRate = %v, want ~0.2", got)
	}
	if got := s.ThinkingShare; got < 0.19 || got > 0.21 {
		t.Errorf("ThinkingShare = %v, want ~0.2", got)
	}
}

func TestTracker_DivisionByZeroGuard(t *testing.T) {
	tr := NewTracker("sess_abc")
	s := tr.Snapshot() // never recorded anything
	if s.CacheHitRate != 0 || s.ThinkingShare != 0 || s.LatencyMsAvg != 0 {
		t.Errorf("derived ratios should be 0 on empty tracker: %+v", s)
	}
}

func TestTracker_SnapshotIsDeepCopy(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RecordLLMCall("opus", "", &types.Usage{InputTokens: 5}, 0)
	s := tr.Snapshot()
	if len(s.PerModel) != 1 {
		t.Fatalf("PerModel len = %d", len(s.PerModel))
	}
	s.PerModel[0].InputTokens = 9999
	s2 := tr.Snapshot()
	if s2.PerModel[0].InputTokens != 5 {
		t.Errorf("Snapshot shared slice memory: got %d, want 5", s2.PerModel[0].InputTokens)
	}
}
