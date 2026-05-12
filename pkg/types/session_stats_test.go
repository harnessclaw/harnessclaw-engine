package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionStats_JSONRoundTrip(t *testing.T) {
	in := SessionStats{
		SessionID: "sess_abc",
		UpdatedAt: time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC),

		InputTokens:    100,
		OutputTokens:   50,
		LatencyMsTotal: 1800,
		LatencyMsAvg:   900,

		CacheReadTokens:  20,
		CacheWriteTokens: 5,
		CacheHitRate:     0.16,
		ThinkingTokens:   8,
		ThinkingShare:    0.16,

		ContextWindow: ContextWindowStats{
			Used: 120, Limit: 200000, History: 80, ToolResults: 30, SystemPrompt: 10,
		},
		PerModel: []ModelStats{{
			Model: "claude-opus-4-7", InputTokens: 100, OutputTokens: 50,
			CacheReadTokens: 20, CacheWriteTokens: 5, ThinkingTokens: 8, LLMCalls: 2,
		}},
		SubAgents: []SubAgentStats{{
			AgentRunID: "run_e5", AgentID: "sub_e5", AgentType: "researcher",
			Model:       "claude-sonnet-4-6",
			InputTokens: 40, OutputTokens: 10, CacheReadTokens: 8, CacheWriteTokens: 0,
			ThinkingTokens: 0, TotalTokens: 50, LLMCalls: 1, DurationMs: 1200,
			Status: "completed",
		}},
		LLMCalls:  2,
		ToolCalls: 3,
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionStats
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SessionID != in.SessionID {
		t.Errorf("SessionID mismatch: %q", out.SessionID)
	}
	if len(out.PerModel) != 1 || out.PerModel[0].Model != "claude-opus-4-7" {
		t.Errorf("PerModel round-trip wrong: %+v", out.PerModel)
	}
	if len(out.SubAgents) != 1 || out.SubAgents[0].Status != "completed" {
		t.Errorf("SubAgents round-trip wrong: %+v", out.SubAgents)
	}
	if out.ContextWindow.SystemPrompt != 10 {
		t.Errorf("ContextWindow.SystemPrompt = %d, want 10", out.ContextWindow.SystemPrompt)
	}
}

func TestSessionStats_EmptyMarshalsToValidJSON(t *testing.T) {
	var in SessionStats
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionStats
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal of %s: %v", string(b), err)
	}
}
