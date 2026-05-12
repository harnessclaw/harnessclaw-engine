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

func TestTracker_StartFinishSubAgent_TableLifecycle(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.StartSubAgent("run_e5", "sub_e5", "researcher", "sonnet")
	tr.RecordLLMCall("sonnet", "run_e5", &types.Usage{InputTokens: 40, OutputTokens: 10}, 100)
	tr.FinishSubAgent("run_e5", "completed", 1200)

	s := tr.Snapshot()
	if len(s.SubAgents) != 1 {
		t.Fatalf("SubAgents len = %d", len(s.SubAgents))
	}
	row := s.SubAgents[0]
	if row.AgentRunID != "run_e5" || row.AgentID != "sub_e5" {
		t.Errorf("identity wrong: %+v", row)
	}
	if row.AgentType != "researcher" || row.Model != "sonnet" {
		t.Errorf("type/model wrong: %+v", row)
	}
	if row.InputTokens != 40 || row.OutputTokens != 10 || row.TotalTokens != 50 {
		t.Errorf("tokens wrong: %+v", row)
	}
	if row.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1", row.LLMCalls)
	}
	if row.Status != "completed" || row.DurationMs != 1200 {
		t.Errorf("finish fields wrong: %+v", row)
	}
}

func TestTracker_StartSubAgent_OrderPreserved(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.StartSubAgent("r1", "a1", "t", "m")
	tr.StartSubAgent("r2", "a2", "t", "m")
	tr.StartSubAgent("r3", "a3", "t", "m")
	s := tr.Snapshot()
	if len(s.SubAgents) != 3 {
		t.Fatalf("len = %d", len(s.SubAgents))
	}
	want := []string{"r1", "r2", "r3"}
	for i, w := range want {
		if s.SubAgents[i].AgentRunID != w {
			t.Errorf("position %d: got %q, want %q", i, s.SubAgents[i].AgentRunID, w)
		}
	}
}

func TestTracker_StartSubAgent_IdempotentOnSameRunID(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.StartSubAgent("r1", "a1", "t", "m1")
	tr.StartSubAgent("r1", "a1", "t", "m2") // second call should not duplicate or overwrite
	s := tr.Snapshot()
	if len(s.SubAgents) != 1 {
		t.Errorf("duplicate StartSubAgent created %d rows", len(s.SubAgents))
	}
	if s.SubAgents[0].Model != "m1" {
		t.Errorf("model overwritten: got %q, want m1", s.SubAgents[0].Model)
	}
}

func TestTracker_FinishSubAgent_UnknownRunIDIsNoop(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.FinishSubAgent("does_not_exist", "failed", 100) // must not panic or append
	s := tr.Snapshot()
	if len(s.SubAgents) != 0 {
		t.Errorf("SubAgents len = %d on no-op finish", len(s.SubAgents))
	}
}

func TestTracker_RecordToolCall_Counts(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RecordToolCall()
	tr.RecordToolCall()
	tr.RecordToolCall()
	if got := tr.Snapshot().ToolCalls; got != 3 {
		t.Errorf("ToolCalls = %d, want 3", got)
	}
}

func TestTracker_UpdateContextWindow_OverwritesSnapshot(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.UpdateContextWindow(100, 200000, 80, 15, 5)
	tr.UpdateContextWindow(120, 200000, 90, 20, 10) // second call wins (most-recent semantics)
	cw := tr.Snapshot().ContextWindow
	if cw.Used != 120 || cw.History != 90 || cw.ToolResults != 20 || cw.SystemPrompt != 10 {
		t.Errorf("ContextWindow not overwritten: %+v", cw)
	}
}

func TestTracker_RecordLLMCall_CrossModelSubAgentMarksMixed(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.StartSubAgent("run_e5", "sub_e5", "researcher", "")
	tr.RecordLLMCall("opus", "run_e5", &types.Usage{InputTokens: 10}, 0)
	tr.RecordLLMCall("sonnet", "run_e5", &types.Usage{InputTokens: 10}, 0)
	s := tr.Snapshot()
	if s.SubAgents[0].Model != "mixed" {
		t.Errorf("Model = %q, want mixed", s.SubAgents[0].Model)
	}
}

func TestTracker_RestoreFrom_RehydratesEmptyTracker(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RestoreFrom(types.SessionStats{
		SessionID:    "sess_abc",
		InputTokens:  100,
		OutputTokens: 50,
		LLMCalls:     2,
		PerModel:     []types.ModelStats{{Model: "opus", InputTokens: 100, LLMCalls: 2}},
		SubAgents: []types.SubAgentStats{
			{AgentRunID: "r1", AgentID: "a1", AgentType: "researcher",
				Model: "sonnet", InputTokens: 30, OutputTokens: 10, TotalTokens: 40,
				Status: "completed", LLMCalls: 1, DurationMs: 500},
		},
	})

	// Subsequent record should add on top, not overwrite.
	tr.RecordLLMCall("opus", "r1", &types.Usage{InputTokens: 50}, 100)

	s := tr.Snapshot()
	if s.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want 150", s.InputTokens)
	}
	if len(s.PerModel) != 1 || s.PerModel[0].InputTokens != 150 {
		t.Errorf("PerModel not merged: %+v", s.PerModel)
	}
	if len(s.SubAgents) != 1 || s.SubAgents[0].InputTokens != 80 {
		t.Errorf("SubAgents not merged: %+v", s.SubAgents)
	}
}

func TestTracker_RestoreFrom_IgnoredOnNonEmptyTracker(t *testing.T) {
	tr := NewTracker("sess_abc")
	tr.RecordLLMCall("opus", "", &types.Usage{InputTokens: 7}, 0)
	tr.RestoreFrom(types.SessionStats{InputTokens: 9999}) // must NOT clobber
	if got := tr.Snapshot().InputTokens; got != 7 {
		t.Errorf("non-empty tracker was overwritten: got %d, want 7", got)
	}
}
