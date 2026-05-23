package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeWebSearchTool is a minimal stub that reports itself as "web_search".
// Used to construct an engine that has a search tool registered, so the
// capability-gap detector should stay silent.
type fakeWebSearchTool struct{ tool.BaseTool }

func (f *fakeWebSearchTool) Name() string                { return "web_search" }
func (f *fakeWebSearchTool) Description() string         { return "fake web search for test" }
func (f *fakeWebSearchTool) IsReadOnly() bool            { return true }
func (f *fakeWebSearchTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f *fakeWebSearchTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "stub"}, nil
}

// researcherDef is a minimal TierSubAgent definition that declares both
// web_search and tavily_search in its AllowedTools. The detector fires when
// neither is available at runtime.
var researcherDef = &agent.AgentDefinition{
	Name: "researcher-test",
	Tier: agent.TierSubAgent,
	AllowedTools: []string{
		"web_search", "tavily_search", "ArtifactRead", "ArtifactWrite",
		"submit_task_result", "escalate_to_planner",
	},
	OutputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result": map[string]any{"type": "string"},
		},
	},
}

// drainForSystemNotice scans ch for up to timeout duration looking for the
// first EngineEventSystemNotice. Returns nil if none arrives within timeout.
func drainForSystemNotice(t *testing.T, ch <-chan types.EngineEvent, timeout time.Duration) *types.SystemNotice {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if ev.Type == types.EngineEventSystemNotice && ev.SystemNotice != nil {
				return ev.SystemNotice
			}
		case <-deadline:
			return nil
		}
	}
}

// newGapTestEngine builds a QueryEngine with the researcherDef registered.
// Extra tools (e.g. fakeWebSearchTool) can be passed to make search available.
func newGapTestEngine(t *testing.T, prov *subagentMockProvider, extraTools ...tool.Tool) *QueryEngine {
	t.Helper()
	eng := newSubagentTestEngine(prov, extraTools...)
	defReg := agent.NewAgentDefinitionRegistry()
	if err := defReg.Register(researcherDef); err != nil {
		t.Fatalf("Register researcherDef: %v", err)
	}
	eng.defRegistry = defReg
	return eng
}

// newEndTurnResponse returns a single end_turn provider response.
func newEndTurnResponse() subagentMockResponse {
	return subagentMockResponse{
		text:       "ok",
		stopReason: "end_turn",
		usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
	}
}

// spawnResearcher runs SpawnSync in a goroutine for the researcher-test agent.
// It closes parentOut when done and sends any SpawnSync error on errCh.
func spawnResearcher(
	t *testing.T,
	eng *QueryEngine,
	parentOut chan types.EngineEvent,
	sessionID string,
) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	ctx := emit.WithTrace(context.Background(), &emit.TraceContext{
		TraceID:   "tr_gap_test",
		Sequencer: emit.NewSequencer(),
	})
	go func() {
		_, err := eng.SpawnSync(ctx, &agent.SpawnConfig{
			Prompt:          "fact-check this claim",
			AgentType:       tool.AgentTypeSync,
			SubagentType:    "researcher-test",
			Description:     "test agent",
			ParentSessionID: sessionID,
			ParentOut:       parentOut,
		})
		close(parentOut)
		errCh <- err
	}()
	return errCh
}

// TestSubAgentSpawn_SearchGap_EmitsSystemNotice verifies that spawning a
// TierSubAgent that declares web_search/tavily_search when neither is registered
// in the engine causes exactly one EngineEventSystemNotice to appear on
// ParentOut with the expected topic, title, icon, and agent name in Summary.
func TestSubAgentSpawn_SearchGap_EmitsSystemNotice(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{newEndTurnResponse()},
	}
	eng := newGapTestEngine(t, prov) // no search tools registered

	parentOut := make(chan types.EngineEvent, 64)
	errCh := spawnResearcher(t, eng, parentOut, "sess-int-1")

	notice := drainForSystemNotice(t, parentOut, 5*time.Second)

	if err := <-errCh; err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if notice == nil {
		t.Fatal("expected EngineEventSystemNotice on parentOut, got none")
	}
	if notice.Topic != "search_capability_gap" {
		t.Errorf("Topic = %q, want %q", notice.Topic, "search_capability_gap")
	}
	if notice.Title != "搜索能力不可用" {
		t.Errorf("Title = %q, want %q", notice.Title, "搜索能力不可用")
	}
	if notice.Icon != "warning" {
		t.Errorf("Icon = %q, want %q", notice.Icon, "warning")
	}
	if !containsStr(notice.Summary, "researcher-test") {
		t.Errorf("Summary %q does not mention agent name %q", notice.Summary, "researcher-test")
	}
}

// TestSubAgentSpawn_SearchGap_DedupesPerSession verifies that spawning the
// same researcher-test agent twice within the same parent session ID emits
// exactly one EngineEventSystemNotice total — the second spawn is deduped.
func TestSubAgentSpawn_SearchGap_DedupesPerSession(t *testing.T) {
	// Two spawns require two provider responses.
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			newEndTurnResponse(),
			newEndTurnResponse(),
		},
	}
	eng := newGapTestEngine(t, prov)

	// Collect all events across both spawns.
	allEvents := make(chan types.EngineEvent, 128)

	ctx := emit.WithTrace(context.Background(), &emit.TraceContext{
		TraceID:   "tr_gap_dedupe",
		Sequencer: emit.NewSequencer(),
	})

	cfg := &agent.SpawnConfig{
		Prompt:          "fact-check this claim",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "researcher-test",
		Description:     "test agent",
		ParentSessionID: "sess-int-dedupe",
		ParentOut:       allEvents,
	}

	// First spawn.
	if _, err := eng.SpawnSync(ctx, cfg); err != nil {
		t.Fatalf("first SpawnSync error: %v", err)
	}

	// Second spawn — same ParentOut channel, same session.
	cfg2 := *cfg // shallow copy keeps ParentSessionID and ParentOut identical
	if _, err := eng.SpawnSync(ctx, &cfg2); err != nil {
		t.Fatalf("second SpawnSync error: %v", err)
	}

	// Drain remaining buffered events (no goroutine to close the channel,
	// so we time-out instead of ranging).
	close(allEvents)
	var notices int
	for ev := range allEvents {
		if ev.Type == types.EngineEventSystemNotice && ev.SystemNotice != nil {
			notices++
		}
	}

	if notices != 1 {
		t.Errorf("expected exactly 1 SystemNotice across two spawns (dedupe), got %d", notices)
	}
}

// TestSubAgentSpawn_SearchAvailable_NoNotice verifies that no
// EngineEventSystemNotice is emitted when the engine has web_search registered.
func TestSubAgentSpawn_SearchAvailable_NoNotice(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{newEndTurnResponse()},
	}
	eng := newGapTestEngine(t, prov, &fakeWebSearchTool{}) // web_search is available

	parentOut := make(chan types.EngineEvent, 64)
	errCh := spawnResearcher(t, eng, parentOut, "sess-int-no-notice")

	if err := <-errCh; err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	// Drain all events; none should be a SystemNotice.
	for ev := range parentOut {
		if ev.Type == types.EngineEventSystemNotice {
			t.Errorf("unexpected EngineEventSystemNotice when web_search is available: %+v", ev.SystemNotice)
		}
	}
}

// containsStr reports whether s contains sub.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
