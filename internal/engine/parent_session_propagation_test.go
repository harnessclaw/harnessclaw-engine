package engine

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/stats"
	ptypes "harnessclaw-go/pkg/types"
)

// TestStatsProvider_SubAgentSessionPropagatesFromCtx asserts that when a
// stats.StatsProvider wraps a provider and the caller's ctx carries a
// SessionID, the Chat() call attributes token usage to that session's tracker.
//
// This test exercises the stats_provider path that the executor fix (Task 1.2)
// enables: with TUC injected, Specialists builds SpawnConfig.ParentSessionID
// from the session, the sub-agent ctx gets the SessionID, and stats_provider
// correctly attributes the spend.
func TestStatsProvider_SubAgentSessionPropagatesFromCtx(t *testing.T) {
	reg := sessionstats.NewRegistry()

	inner := &engineFakeProv{events: []ptypes.StreamEvent{{
		Type:  ptypes.StreamEventMessageEnd,
		Usage: &ptypes.Usage{InputTokens: 10, OutputTokens: 5},
	}}}

	sp := stats.New(inner, reg)

	ctx := sessionstats.WithSessionID(context.Background(), "sess_parent_xyz")
	stream, err := sp.Chat(ctx, &provider.ChatRequest{
		Model:     "sonnet",
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// Drain the stream to trigger the MessageEnd callback.
	for range stream.Events {
	}

	tr := reg.Get("sess_parent_xyz")
	if tr == nil {
		t.Fatal("tracker not created for sess_parent_xyz")
	}
	s := tr.Snapshot()

	if s.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", s.InputTokens)
	}
	if s.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", s.OutputTokens)
	}
	if s.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1", s.LLMCalls)
	}
}
