package engine

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/stats"
	ptypes "harnessclaw-go/pkg/types"
)

// TestSubAgentTokenAttribution_L3DualWrite asserts Plan B: an L3 sub-agent
// (writer/researcher) whose immediate parent is the specialists sub_session
// writes its token data into BOTH the specialists tracker AND emma's root
// tracker.
//
// Setup mirrors production:
//   emma session          → root tracker
//   specialists sub_session → parent (immediate) tracker
//   L3 writer agent       → agentRunID = "agent_writer_xyz"
//
// After one Chat() call the root (emma) tracker must contain the same L3
// SubAgent row data as the parent (specialists) tracker.
func TestSubAgentTokenAttribution_L3DualWrite(t *testing.T) {
	const (
		emmaSession         = "sess_emma_dual_001"
		specialistsSub      = "sess_emma_dual_001_sub_spec01"
		l3RunID             = "agent_writer_xyz"
	)

	reg := sessionstats.NewRegistry()

	// Create both trackers ahead of time (SpawnSync's StartSubAgent dual-write
	// requires the root tracker to already exist — it uses Get, not GetOrCreate).
	emmaTracker := reg.GetOrCreate(emmaSession)
	specTracker := reg.GetOrCreate(specialistsSub)

	// Simulate SpawnSync opening a row on the parent tracker AND the root tracker
	// (the dual-write block added to subagent.go).
	specTracker.StartSubAgent(l3RunID, l3RunID, "writer", "")
	emmaTracker.StartSubAgent(l3RunID, l3RunID, "writer", "")

	// Stats-decorated provider with one MessageEnd event.
	inner := &engineFakeProv{events: []ptypes.StreamEvent{{
		Type: ptypes.StreamEventMessageEnd,
		Usage: &ptypes.Usage{
			InputTokens:  300,
			OutputTokens: 90,
			CacheRead:    20,
		},
		Model: "sonnet-3-7",
	}}}
	sp := stats.New(inner, reg)

	// ctx carries: specialists sub_session as SessionID + emma as RootSessionID.
	ctx := sessionstats.WithSessionID(context.Background(), specialistsSub)
	ctx = sessionstats.WithRootSessionID(ctx, emmaSession)
	ctx = sessionstats.WithAgentRunID(ctx, l3RunID)

	stream, err := sp.Chat(ctx, &provider.ChatRequest{MaxTokens: 1024})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream.Events {
	}

	// --- Assertions on the specialists (immediate parent) tracker ---
	specSnap := specTracker.Snapshot()
	if len(specSnap.SubAgents) != 1 {
		t.Fatalf("specialists tracker: SubAgents len = %d, want 1", len(specSnap.SubAgents))
	}
	specRow := specSnap.SubAgents[0]
	if specRow.InputTokens != 300 {
		t.Errorf("specialists tracker: InputTokens = %d, want 300", specRow.InputTokens)
	}
	if specRow.OutputTokens != 90 {
		t.Errorf("specialists tracker: OutputTokens = %d, want 90", specRow.OutputTokens)
	}
	if specRow.LLMCalls != 1 {
		t.Errorf("specialists tracker: LLMCalls = %d, want 1", specRow.LLMCalls)
	}

	// --- Assertions on the emma (root) tracker ---
	emmaSnap := emmaTracker.Snapshot()
	if len(emmaSnap.SubAgents) != 1 {
		t.Fatalf("emma tracker: SubAgents len = %d, want 1 (dual-write must open L3 row)", len(emmaSnap.SubAgents))
	}
	emmaRow := emmaSnap.SubAgents[0]
	if emmaRow.AgentRunID != l3RunID {
		t.Errorf("emma tracker: AgentRunID = %q, want %q", emmaRow.AgentRunID, l3RunID)
	}
	if emmaRow.InputTokens != 300 {
		t.Errorf("emma tracker: InputTokens = %d, want 300 (same as parent row)", emmaRow.InputTokens)
	}
	if emmaRow.OutputTokens != 90 {
		t.Errorf("emma tracker: OutputTokens = %d, want 90 (same as parent row)", emmaRow.OutputTokens)
	}
	if emmaRow.LLMCalls != 1 {
		t.Errorf("emma tracker: LLMCalls = %d, want 1", emmaRow.LLMCalls)
	}
}

// TestSubAgentTokenAttribution_EndToEnd asserts the full attribution chain:
//  1. parent tracker exists for "sess_emma_001"
//  2. StartSubAgent opens a row keyed by agentRunID
//  3. A Chat() call decorated by stats.New — with ctx carrying both the
//     parent SessionID and the sub-agent RunID — increments the sub-agent
//     row's token counters.
//
// This is the chain that the executor fix (Task 1.2) unblocks in production:
// Specialists reads TUC → builds SpawnConfig with ParentSessionID → the
// sub-agent ctx carries WithSessionID + WithAgentRunID → stats.New sees both
// → RecordLLMCall attributes to the matching SubAgentStats row.
func TestSubAgentTokenAttribution_EndToEnd(t *testing.T) {
	const (
		parentSession = "sess_emma_001"
		subRunID      = "agent_specialists_abc"
	)

	// 1. Create a registry and a parent tracker.
	reg := sessionstats.NewRegistry()
	tracker := reg.GetOrCreate(parentSession)

	// 2. Open a sub-agent row — mimics what subagent.go does after SpawnSync.
	tracker.StartSubAgent(subRunID, subRunID, "specialists", "")

	// Verify the row exists but is empty before the Chat call.
	snap0 := tracker.Snapshot()
	if len(snap0.SubAgents) != 1 {
		t.Fatalf("expected 1 sub-agent row after StartSubAgent, got %d", len(snap0.SubAgents))
	}
	if snap0.SubAgents[0].LLMCalls != 0 {
		t.Errorf("LLMCalls should be 0 before any Chat, got %d", snap0.SubAgents[0].LLMCalls)
	}

	// 3. Stats-decorated provider sends one MessageEnd with Usage.
	inner := &engineFakeProv{events: []ptypes.StreamEvent{{
		Type: ptypes.StreamEventMessageEnd,
		Usage: &ptypes.Usage{
			InputTokens:  300,
			OutputTokens: 80,
			CacheRead:    50,
		},
		Model: "sonnet-3-7",
	}}}
	sp := stats.New(inner, reg)

	// Ctx carries both the parent session and the sub-agent run ID —
	// exactly what the production path sets up after the fix.
	ctx := sessionstats.WithSessionID(context.Background(), parentSession)
	ctx = sessionstats.WithAgentRunID(ctx, subRunID)

	stream, err := sp.Chat(ctx, &provider.ChatRequest{
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream.Events {
	}

	// 4. Assert the parent tracker's Snapshot contains exactly 1 sub-agent row
	//    with the expected token counts.
	snap := tracker.Snapshot()

	if len(snap.SubAgents) != 1 {
		t.Fatalf("SubAgents len = %d, want 1", len(snap.SubAgents))
	}
	sa := snap.SubAgents[0]

	if sa.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", sa.InputTokens)
	}
	if sa.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", sa.OutputTokens)
	}
	if sa.AgentType != "specialists" {
		t.Errorf("AgentType = %q, want %q", sa.AgentType, "specialists")
	}
	if sa.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1", sa.LLMCalls)
	}
}
