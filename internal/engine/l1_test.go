package engine

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/pkg/types"
)

func TestL1Engine_DefaultConfig(t *testing.T) {
	inner := newSubagentTestEngine(&subagentMockProvider{})
	l1 := NewL1Engine(inner, L1Config{}, zap.NewNop())

	cfg := l1.Config()
	if cfg.Profile != prompt.EmmaProfile {
		t.Errorf("default Profile = %v, want EmmaProfile", cfg.Profile)
	}
	if cfg.DisplayName != "emma" {
		t.Errorf("default DisplayName = %q, want emma", cfg.DisplayName)
	}
	// AllowedTools length & exact membership are asserted in
	// TestL1Engine_DefaultL1Config — here we just sanity-check non-empty
	// and that the single delegation entry (Specialists) is present.
	if len(cfg.AllowedTools) < 2 {
		t.Errorf("default AllowedTools = %v, want at least Specialists + a search tool", cfg.AllowedTools)
	}
	hasSpecialists := false
	for _, n := range cfg.AllowedTools {
		if n == "Specialists" {
			hasSpecialists = true
		}
		if n == "Agent" || n == "Orchestrate" {
			t.Errorf("L1 must not expose %q (L2-internal)", n)
		}
	}
	if !hasSpecialists {
		t.Errorf("default AllowedTools missing Specialists: %v", cfg.AllowedTools)
	}
	if cfg.MaxTurns != 10 {
		t.Errorf("default MaxTurns = %d, want 10", cfg.MaxTurns)
	}
}

func TestL1Engine_AppliesToInnerConfig(t *testing.T) {
	inner := newSubagentTestEngine(&subagentMockProvider{})
	originalMaxTurns := inner.config.MaxTurns

	_ = NewL1Engine(inner, L1Config{
		Profile:      prompt.EmmaProfile,
		DisplayName:  "emma",
		AllowedTools: []string{"Agent", "Orchestrate"},
		MaxTurns:     7,
	}, zap.NewNop())

	if inner.config.MainAgentProfile != prompt.EmmaProfile {
		t.Errorf("inner.config.MainAgentProfile not set")
	}
	if inner.config.MainAgentDisplayName != "emma" {
		t.Errorf("inner.config.MainAgentDisplayName = %q", inner.config.MainAgentDisplayName)
	}
	if got := inner.config.MainAgentAllowedTools; len(got) != 2 || got[0] != "Agent" {
		t.Errorf("inner.config.MainAgentAllowedTools = %v", got)
	}
	if inner.config.MainAgentMaxTurns != 7 {
		t.Errorf("inner.config.MainAgentMaxTurns = %d, want 7", inner.config.MainAgentMaxTurns)
	}
	if inner.config.MaxTurns != originalMaxTurns {
		t.Errorf("inner.config.MaxTurns mutated: was %d, now %d (sub-agents would be impacted)",
			originalMaxTurns, inner.config.MaxTurns)
	}
	if inner.promptProfile != prompt.EmmaProfile {
		t.Errorf("inner.promptProfile not updated to emma")
	}
}

func TestL1Engine_CustomProfile(t *testing.T) {
	custom := &prompt.AgentProfile{Name: "custom-leader", Description: "test"}
	inner := newSubagentTestEngine(&subagentMockProvider{})

	l1 := NewL1Engine(inner, L1Config{
		Profile:     custom,
		DisplayName: "Sara",
	}, zap.NewNop())

	if l1.Config().Profile != custom {
		t.Error("custom profile not honored")
	}
	if l1.Config().DisplayName != "Sara" {
		t.Errorf("custom display name not honored: %q", l1.Config().DisplayName)
	}
	if inner.promptProfile != custom {
		t.Error("inner.promptProfile should reflect custom profile")
	}
	if inner.config.MainAgentDisplayName != "Sara" {
		t.Errorf("inner display name not propagated: %q", inner.config.MainAgentDisplayName)
	}
}

func TestL1Engine_ConfigReturnsCopy(t *testing.T) {
	inner := newSubagentTestEngine(&subagentMockProvider{})
	l1 := NewL1Engine(inner, L1Config{
		AllowedTools: []string{"Agent", "Orchestrate"},
	}, zap.NewNop())

	cfg := l1.Config()
	cfg.AllowedTools[0] = "MUTATED"

	if l1.Config().AllowedTools[0] != "Agent" {
		t.Errorf("Config() did not return a defensive copy; AllowedTools[0]=%q",
			l1.Config().AllowedTools[0])
	}
}

func TestL1Engine_Inner(t *testing.T) {
	inner := newSubagentTestEngine(&subagentMockProvider{})
	l1 := NewL1Engine(inner, L1Config{}, zap.NewNop())
	if l1.Inner() != inner {
		t.Error("Inner() should return the wrapped QueryEngine")
	}
}

func TestL1Engine_DefaultL1Config(t *testing.T) {
	cfg := DefaultL1Config()
	if cfg.Profile != prompt.EmmaProfile {
		t.Error("DefaultL1Config Profile mismatch")
	}
	if cfg.DisplayName != "emma" {
		t.Error("DefaultL1Config DisplayName mismatch")
	}
	// L1 palette in the 3-tier architecture: a single delegation entry
	// (Specialists), light search for context (WebSearch/TavilySearch),
	// and clarification (AskUserQuestion). Agent / Orchestrate are NOT
	// in this list — they are L2-internal.
	wantTools := map[string]bool{
		"Specialists":     true,
		"WebSearch":       true,
		"TavilySearch":    true,
		"AskUserQuestion": true,
	}
	if len(cfg.AllowedTools) != len(wantTools) {
		t.Errorf("DefaultL1Config AllowedTools length = %d, want %d",
			len(cfg.AllowedTools), len(wantTools))
	}
	for _, name := range cfg.AllowedTools {
		if !wantTools[name] {
			t.Errorf("unexpected tool in default L1 palette: %s", name)
		}
		delete(wantTools, name)
	}
	if len(wantTools) > 0 {
		t.Errorf("default L1 palette missing tools: %v", wantTools)
	}
	// Agent / Orchestrate must NOT be exposed at L1.
	for _, n := range cfg.AllowedTools {
		if n == "Agent" || n == "Orchestrate" {
			t.Errorf("L1 palette must not expose %q (L2-internal)", n)
		}
	}
	if cfg.MaxTurns != 10 {
		t.Errorf("DefaultL1Config MaxTurns = %d", cfg.MaxTurns)
	}
}

// TestL1Engine_ImplementsEngineInterface confirms the wrapper satisfies the
// engine.Engine contract that the router relies on.
func TestL1Engine_ImplementsEngineInterface(t *testing.T) {
	var _ Engine = (*L1Engine)(nil)
}

// TestL1Engine_PassthroughMethods exercises the trivial passthroughs to
// ensure compile-time wiring is correct (any signature drift would fail
// here at build time, and runtime correctness is delegated to the inner
// engine's own tests).
func TestL1Engine_PassthroughMethods(t *testing.T) {
	inner := newSubagentTestEngine(&subagentMockProvider{})
	l1 := NewL1Engine(inner, L1Config{}, zap.NewNop())

	// AbortSession is a passthrough — whether the inner engine returns nil
	// or "no active query" for a non-existent session is its concern. We
	// only verify the call compiles and reaches inner.
	_ = l1.AbortSession(context.Background(), "no-such-session")

	// SubmitToolResult on a non-pending tool_use_id returns no error today
	// (silently dropped); we just verify the call compiles and reaches inner.
	_ = l1.SubmitToolResult(context.Background(), "no-such-session",
		&types.ToolResultPayload{ToolUseID: "missing"})

	_ = l1.SubmitPermissionResult(context.Background(), "no-such-session",
		&types.PermissionResponse{RequestID: "missing", Approved: false})
}
