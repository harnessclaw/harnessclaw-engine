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
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// --- Minimal mock provider ---
//
// These tests verify configuration application only — they never drive a
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

func newTestEngineWithOpts(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	logger := zap.NewNop()
	store := memory.New()
	mgr := session.NewManager(store, logger, 30*time.Minute)
	cmdReg := command.NewRegistry()
	reg := tool.NewRegistry()

	cfg := Config{
		MaxTurns:                50,
		AutoCompactThreshold:    0.8,
		ToolTimeout:             30 * time.Second,
		MaxTokens:               4096,
		SystemPrompt:            "You are a test assistant.",
		ClientTools:             false,
		DisableStepDecisionGate: true,
	}

	return New(&emmaMockProvider{}, reg, mgr, nil, permission.BypassChecker{}, logger, cfg, cmdReg, opts...)
}

func TestEngine_DefaultEmmaConfigOverlay(t *testing.T) {
	e := newTestEngineWithOpts(t, WithEmmaConfig(EmmaConfig{}))

	cfg := e.Config()
	if cfg.MainAgentProfile != prompt.EmmaProfile {
		t.Errorf("default Profile = %v, want EmmaProfile", cfg.MainAgentProfile)
	}
	if cfg.MainAgentDisplayName != "emma" {
		t.Errorf("default DisplayName = %q, want emma", cfg.MainAgentDisplayName)
	}
	if len(cfg.MainAgentAllowedTools) < 2 {
		t.Errorf("default AllowedTools = %v, want at least scheduler + a search tool", cfg.MainAgentAllowedTools)
	}
	hasScheduler := false
	for _, n := range cfg.MainAgentAllowedTools {
		if n == "scheduler" {
			hasScheduler = true
		}
		if n == "Agent" || n == "orchestrate" {
			t.Errorf("emma must not expose %q (L2-internal)", n)
		}
	}
	if !hasScheduler {
		t.Errorf("default AllowedTools missing scheduler: %v", cfg.MainAgentAllowedTools)
	}
	if cfg.MainAgentMaxTurns != 10 {
		t.Errorf("default MaxTurns = %d, want 10", cfg.MainAgentMaxTurns)
	}
}

func TestEngine_AppliesExplicitEmmaOverlay(t *testing.T) {
	e := newTestEngineWithOpts(t, WithEmmaConfig(EmmaConfig{
		Profile:      prompt.EmmaProfile,
		DisplayName:  "emma",
		AllowedTools: []string{"Agent", "orchestrate"},
		MaxTurns:     7,
	}))

	cfg := e.Config()
	if cfg.MainAgentProfile != prompt.EmmaProfile {
		t.Errorf("MainAgentProfile not set")
	}
	if cfg.MainAgentDisplayName != "emma" {
		t.Errorf("MainAgentDisplayName = %q", cfg.MainAgentDisplayName)
	}
	if got := cfg.MainAgentAllowedTools; len(got) != 2 || got[0] != "Agent" {
		t.Errorf("MainAgentAllowedTools = %v", got)
	}
	if cfg.MainAgentMaxTurns != 7 {
		t.Errorf("MainAgentMaxTurns = %d, want 7", cfg.MainAgentMaxTurns)
	}
	if e.PromptProfile() != prompt.EmmaProfile {
		t.Errorf("PromptProfile not updated to emma")
	}
}

func TestEngine_CustomProfile(t *testing.T) {
	custom := &prompt.AgentProfile{Name: "custom-leader", Description: "test"}
	e := newTestEngineWithOpts(t, WithEmmaConfig(EmmaConfig{
		Profile:     custom,
		DisplayName: "Sara",
	}))

	cfg := e.Config()
	if cfg.MainAgentProfile != custom {
		t.Error("custom profile not honored")
	}
	if cfg.MainAgentDisplayName != "Sara" {
		t.Errorf("custom display name not honored: %q", cfg.MainAgentDisplayName)
	}
	if e.PromptProfile() != custom {
		t.Error("PromptProfile should reflect custom profile")
	}
}

func TestEngine_DefaultEmmaConfigConstants(t *testing.T) {
	cfg := DefaultEmmaConfig()
	if cfg.Profile != prompt.EmmaProfile {
		t.Error("DefaultEmmaConfig Profile mismatch")
	}
	if cfg.DisplayName != "emma" {
		t.Error("DefaultEmmaConfig DisplayName mismatch")
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
		t.Errorf("DefaultEmmaConfig AllowedTools length = %d, want %d",
			len(cfg.AllowedTools), len(wantTools))
	}
	for _, name := range cfg.AllowedTools {
		if !wantTools[name] {
			t.Errorf("unexpected tool in default emma palette: %s", name)
		}
		delete(wantTools, name)
	}
	if len(wantTools) > 0 {
		t.Errorf("default emma palette missing tools: %v", wantTools)
	}
	for _, n := range cfg.AllowedTools {
		if n == "Agent" || n == "orchestrate" {
			t.Errorf("emma palette must not expose %q (L2-internal)", n)
		}
	}
	if cfg.MaxTurns != 10 {
		t.Errorf("DefaultEmmaConfig MaxTurns = %d", cfg.MaxTurns)
	}
}

// TestEngine_ImplementsEngineInterface confirms the engine satisfies the
// engine.Engine contract that the router relies on.
func TestEngine_ImplementsEngineInterface(t *testing.T) {
	var _ engine.Engine = (*Engine)(nil)
}

// TestEngine_PassthroughMethods exercises the trivial submit paths to
// ensure compile-time wiring is correct.
func TestEngine_PassthroughMethods(t *testing.T) {
	e := newTestEngineWithOpts(t, WithEmmaConfig(EmmaConfig{}))

	_ = e.AbortSession(context.Background(), "no-such-session")
	_ = e.SubmitToolResult(context.Background(), "no-such-session",
		&types.ToolResultPayload{ToolUseID: "missing"})
	_ = e.SubmitPermissionResult(context.Background(), "no-such-session",
		&types.PermissionResponse{RequestID: "missing", Approved: false})
}
