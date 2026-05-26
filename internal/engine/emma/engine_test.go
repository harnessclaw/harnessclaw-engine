package emma

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// --- Minimal mock provider ---
//
// L1Engine tests verify configuration application only — they never drive a
// Chat() call, so the mock just satisfies the provider.Provider interface
// without any response wiring.

type emmaMockProvider struct{}

func (m *emmaMockProvider) Name() string { return "mock-emma" }

func (m *emmaMockProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	return nil, nil
}

func (m *emmaMockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func newTestInner() *engine.QueryEngine {
	logger := zap.NewNop()
	store := memory.New()
	bus := event.NewBus()
	mgr := session.NewManager(store, logger, 30*time.Minute)
	cmdReg := command.NewRegistry()
	reg := tool.NewRegistry()

	cfg := engine.QueryEngineConfig{
		MaxTurns:                50,
		AutoCompactThreshold:    0.8,
		ToolTimeout:             30 * time.Second,
		MaxTokens:               4096,
		SystemPrompt:            "You are a test assistant.",
		ClientTools:             false,
		DisableStepDecisionGate: true,
	}

	return engine.NewQueryEngine(&emmaMockProvider{}, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, cmdReg)
}

func TestL1Engine_DefaultConfig(t *testing.T) {
	inner := newTestInner()
	l1 := NewL1Engine(inner, L1Config{}, zap.NewNop())

	cfg := l1.Config()
	if cfg.Profile != prompt.EmmaProfile {
		t.Errorf("default Profile = %v, want EmmaProfile", cfg.Profile)
	}
	if cfg.DisplayName != "emma" {
		t.Errorf("default DisplayName = %q, want emma", cfg.DisplayName)
	}
	if len(cfg.AllowedTools) < 2 {
		t.Errorf("default AllowedTools = %v, want at least scheduler + a search tool", cfg.AllowedTools)
	}
	hasScheduler := false
	for _, n := range cfg.AllowedTools {
		if n == "scheduler" {
			hasScheduler = true
		}
		if n == "Agent" || n == "orchestrate" {
			t.Errorf("L1 must not expose %q (L2-internal)", n)
		}
	}
	if !hasScheduler {
		t.Errorf("default AllowedTools missing scheduler: %v", cfg.AllowedTools)
	}
	if cfg.MaxTurns != 10 {
		t.Errorf("default MaxTurns = %d, want 10", cfg.MaxTurns)
	}
}

func TestL1Engine_AppliesToInnerConfig(t *testing.T) {
	inner := newTestInner()
	originalMaxTurns := inner.Config().MaxTurns

	_ = NewL1Engine(inner, L1Config{
		Profile:      prompt.EmmaProfile,
		DisplayName:  "emma",
		AllowedTools: []string{"Agent", "orchestrate"},
		MaxTurns:     7,
	}, zap.NewNop())

	innerCfg := inner.Config()
	if innerCfg.MainAgentProfile != prompt.EmmaProfile {
		t.Errorf("inner.config.MainAgentProfile not set")
	}
	if innerCfg.MainAgentDisplayName != "emma" {
		t.Errorf("inner.config.MainAgentDisplayName = %q", innerCfg.MainAgentDisplayName)
	}
	if got := innerCfg.MainAgentAllowedTools; len(got) != 2 || got[0] != "Agent" {
		t.Errorf("inner.config.MainAgentAllowedTools = %v", got)
	}
	if innerCfg.MainAgentMaxTurns != 7 {
		t.Errorf("inner.config.MainAgentMaxTurns = %d, want 7", innerCfg.MainAgentMaxTurns)
	}
	if innerCfg.MaxTurns != originalMaxTurns {
		t.Errorf("inner.config.MaxTurns mutated: was %d, now %d (sub-agents would be impacted)",
			originalMaxTurns, innerCfg.MaxTurns)
	}
	if inner.PromptProfile() != prompt.EmmaProfile {
		t.Errorf("inner.promptProfile not updated to emma")
	}
}

func TestL1Engine_CustomProfile(t *testing.T) {
	custom := &prompt.AgentProfile{Name: "custom-leader", Description: "test"}
	inner := newTestInner()

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
	if inner.PromptProfile() != custom {
		t.Error("inner.promptProfile should reflect custom profile")
	}
	if inner.Config().MainAgentDisplayName != "Sara" {
		t.Errorf("inner display name not propagated: %q", inner.Config().MainAgentDisplayName)
	}
}

func TestL1Engine_ConfigReturnsCopy(t *testing.T) {
	inner := newTestInner()
	l1 := NewL1Engine(inner, L1Config{
		AllowedTools: []string{"Agent", "orchestrate"},
	}, zap.NewNop())

	cfg := l1.Config()
	cfg.AllowedTools[0] = "MUTATED"

	if l1.Config().AllowedTools[0] != "Agent" {
		t.Errorf("Config() did not return a defensive copy; AllowedTools[0]=%q",
			l1.Config().AllowedTools[0])
	}
}

func TestL1Engine_Inner(t *testing.T) {
	inner := newTestInner()
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
	wantTools := map[string]bool{
		"scheduler":         true,
		"web_search":        true,
		"tavily_search":     true,
		"ask_user_question": true,
		"read":              true,
		"glob":              true,
		"grep":              true,
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
	for _, n := range cfg.AllowedTools {
		if n == "Agent" || n == "orchestrate" {
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
	var _ engine.Engine = (*L1Engine)(nil)
}

// TestL1Engine_PassthroughMethods exercises the trivial passthroughs to
// ensure compile-time wiring is correct.
func TestL1Engine_PassthroughMethods(t *testing.T) {
	inner := newTestInner()
	l1 := NewL1Engine(inner, L1Config{}, zap.NewNop())

	_ = l1.AbortSession(context.Background(), "no-such-session")
	_ = l1.SubmitToolResult(context.Background(), "no-such-session",
		&types.ToolResultPayload{ToolUseID: "missing"})
	_ = l1.SubmitPermissionResult(context.Background(), "no-such-session",
		&types.PermissionResponse{RequestID: "missing", Approved: false})
}
