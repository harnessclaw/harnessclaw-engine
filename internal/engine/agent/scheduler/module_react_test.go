package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/agent/scheduler"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeReactProvider returns one text turn that immediately ends with
// end_turn. The text is the L2's "summary to emma" — the new react path
// captures this LastMessage as result.Output (was previously the L3's
// "好的老板，我试试看" leaking through legacy meta.json).
type fakeReactProvider struct{}

func (f *fakeReactProvider) Name() string { return "fake-react" }
func (f *fakeReactProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakeReactProvider) SupportsImages() bool { return false }
func (f *fakeReactProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 4)
	ch <- types.StreamEvent{Type: types.StreamEventText, Text: "<summary>L2 完成调度，已派 freelancer，产出 art_abc</summary>"}
	ch <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: "end_turn",
		Usage:      &types.Usage{InputTokens: 5, OutputTokens: 12},
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

// TestRunReactLLM_TerminatesOnEndTurn validates the P1 in-module LLM
// loop wiring: react mode must NOT require Coord, must drive
// loop.Run, and the loop's LastMessage text must surface as the
// parent-visible Output. The pre-P1 path returned the L3 freelancer's
// 30-char opener via runner.runWithSpawnFn → composeOutput; that path
// is no longer hit for react.
func TestRunReactLLM_TerminatesOnEndTurn(t *testing.T) {
	rootDir := t.TempDir()
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)

	promptBuilder := prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop())

	deps := scheduler.Deps{
		Provider:      &fakeReactProvider{},
		Registry:      tool.NewRegistry(),
		SessionMgr:    mgr,
		// Compactor nil — loop tolerates.
		Retryer:       retry.New(retry.DefaultConfig(), zap.NewNop()),
		PromptBuilder: promptBuilder,
		Logger:        zap.NewNop(),
		MaxTokens:     8192,
		ContextWindow: 200000,
		ToolTimeout:   10 * time.Second,
		RootDir:       rootDir,
		// Coord intentionally nil — react path must not require it.
		Spawner: spawn.NewSpawner(zap.NewNop()),
	}
	m := scheduler.New(deps)

	parentSess, err := mgr.GetOrCreate(context.Background(), "parent-sess", "ws", "user")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	cfg := &agent.SpawnConfig{
		Prompt:          "派给专业团：写一份周报",
		SubagentType:    "scheduler",
		ParentSessionID: parentSess.ID,
		RootSessionID:   parentSess.ID,
		CoordinatorMode: "react",
	}

	result, err := m.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.NumTurns == 0 {
		t.Error("NumTurns = 0 — react LLM loop did not execute (regression to legacy 0-turn path)")
	}
	if !strings.Contains(result.Output, "L2 完成调度") {
		t.Errorf("Output should carry the L2 LLM's LastMessage text, got: %q", result.Output)
	}
	if result.Terminal == nil || result.Terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected Terminal.Reason=Completed, got %+v", result.Terminal)
	}
	if result.CoordinatorMode != "react" {
		t.Errorf("CoordinatorMode = %q, want react", result.CoordinatorMode)
	}
}

// TestRunPlanLegacy_RequiresCoord validates that the plan path still
// surfaces a clear error when Coord is missing (vs the old runtime nil
// deref). Run with plan mode + no Coord ⇒ error message.
func TestRunPlanLegacy_RequiresCoord(t *testing.T) {
	rootDir := t.TempDir()
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	promptBuilder := prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop())

	m := scheduler.New(scheduler.Deps{
		Provider:      &fakeReactProvider{},
		Registry:      tool.NewRegistry(),
		SessionMgr:    mgr,
		Retryer:       retry.New(retry.DefaultConfig(), zap.NewNop()),
		PromptBuilder: promptBuilder,
		Logger:        zap.NewNop(),
		RootDir:       rootDir,
		// Coord: nil — plan path must reject explicitly.
	})

	parentSess, _ := mgr.GetOrCreate(context.Background(), "parent-sess", "ws", "user")
	cfg := &agent.SpawnConfig{
		Prompt:          "走 plan 模式",
		SubagentType:    "scheduler",
		ParentSessionID: parentSess.ID,
		RootSessionID:   parentSess.ID,
		CoordinatorMode: "plan",
	}

	_, err := m.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when plan mode used without Coord")
	}
	if !strings.Contains(err.Error(), "plan mode requires Deps.Coord") {
		t.Errorf("error should mention Coord requirement, got: %v", err)
	}
}
