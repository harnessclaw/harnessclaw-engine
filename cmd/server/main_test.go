//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// loadTestConfig loads configs/config.yaml via Viper (same path as real server).
func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	return cfg
}

// buildFullEngine wires exactly the same components as main() —
// real Bifrost provider, real engine, real session manager.
func buildFullEngine(t *testing.T) *engine.QueryEngine {
	t.Helper()
	cfg := loadTestConfig(t)

	logger, _ := zap.NewDevelopment()

	// Provider — same logic as initProvider in main.go (always Bifrost).
	provCfg := cfg.LLM.Providers[cfg.LLM.DefaultProvider]
	prov, err := bifrost.New(bifrost.Config{
		Provider: mapBifrostProvider(cfg.LLM.Bifrost.Provider, cfg.LLM.DefaultProvider),
		Model:    provCfg.Model,
		APIKey:   provCfg.APIKey,
		BaseURL:  provCfg.BaseURL,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("create bifrost adapter: %v", err)
	}

	store := memory.New()
	mgr := session.NewManager(store, logger, cfg.Session.IdleTimeout)
	bus := event.NewBus()
	reg := tool.NewRegistry()
	comp := compact.NewLLMCompactor(prov, logger)
	perm := permission.BypassChecker{}

	engCfg := engine.QueryEngineConfig{
		MaxTurns:             cfg.Engine.MaxTurns,
		AutoCompactThreshold: cfg.Engine.AutoCompactThreshold,
		ToolTimeout:          cfg.Engine.ToolTimeout,
		MaxTokens:            4096,
		SystemPrompt:         "You are a helpful assistant. Be concise.",
	}
	return engine.NewQueryEngine(prov, reg, mgr, comp, perm, bus, logger, engCfg)
}

func collect(t *testing.T, ch <-chan types.EngineEvent, timeout time.Duration) (text string, terminal *types.Terminal, events []types.EngineEvent) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			events = append(events, evt)
			if evt.Type == types.EngineEventText {
				text += evt.Text
			}
			if evt.Type == types.EngineEventDone {
				terminal = evt.Terminal
			}
		case <-timer.C:
			t.Fatalf("timeout (%v) — collected %d events, text=%q", timeout, len(events), text)
		}
	}
}

// =============================================
// Test: 端到端启动 → 真实 API 文本对话
// =============================================

func TestE2E_RealProvider_TextChat(t *testing.T) {
	eng := buildFullEngine(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msg := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Reply with only: hello world"}},
	}
	ch, err := eng.ProcessMessage(ctx, "e2e-text-1", msg)
	if err != nil {
		t.Fatal(err)
	}

	text, terminal, _ := collect(t, ch, 30*time.Second)
	t.Logf("response: %q", text)

	if !strings.Contains(strings.ToLower(text), "hello") {
		t.Errorf("expected 'hello' in response, got %q", text)
	}
	if terminal == nil || terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %+v", terminal)
	}
}

// =============================================
// Test: 端到端启动 → 真实 API 工具调用
// =============================================

type dateTool struct {
	tool.BaseTool
	called bool
}

func (d *dateTool) Name() string        { return "get_date" }
func (d *dateTool) Description() string { return "Returns today's date in YYYY-MM-DD format" }
func (d *dateTool) IsReadOnly() bool    { return true }
func (d *dateTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (d *dateTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	d.called = true
	return &types.ToolResult{Content: "2026-04-05"}, nil
}

func TestE2E_RealProvider_ToolCall(t *testing.T) {
	cfg := loadTestConfig(t)
	logger, _ := zap.NewDevelopment()

	provCfg := cfg.LLM.Providers[cfg.LLM.DefaultProvider]
	prov, err := bifrost.New(bifrost.Config{
		Provider: mapBifrostProvider(cfg.LLM.Bifrost.Provider, cfg.LLM.DefaultProvider),
		Model:    provCfg.Model,
		APIKey:   provCfg.APIKey,
		BaseURL:  provCfg.BaseURL,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("create bifrost adapter: %v", err)
	}

	store := memory.New()
	mgr := session.NewManager(store, logger, cfg.Session.IdleTimeout)
	bus := event.NewBus()
	reg := tool.NewRegistry()
	dt := &dateTool{}
	if err := reg.Register(dt); err != nil {
		t.Fatal(err)
	}

	eng := engine.NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger,
		engine.QueryEngineConfig{
			MaxTurns:             5,
			AutoCompactThreshold: 0.9,
			ToolTimeout:          30 * time.Second,
			MaxTokens:            1024,
			SystemPrompt:         "You are helpful. When asked about today's date, always use the get_date tool.",
		})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	msg := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "What is today's date? Use the get_date tool."}},
	}
	ch, err := eng.ProcessMessage(ctx, "e2e-tool-1", msg)
	if err != nil {
		t.Fatal(err)
	}

	text, terminal, events := collect(t, ch, 60*time.Second)
	t.Logf("response: %q", text)

	// Verify tool was called.
	if !dt.called {
		t.Fatal("expected get_date tool to be called")
	}

	// Verify tool events in stream.
	var starts, ends int
	for _, evt := range events {
		if evt.Type == types.EngineEventToolStart && evt.ToolName == "get_date" {
			starts++
		}
		if evt.Type == types.EngineEventToolEnd && evt.ToolName == "get_date" {
			ends++
		}
	}
	if starts == 0 || ends == 0 {
		t.Errorf("expected tool_start and tool_end events, got starts=%d ends=%d", starts, ends)
	}

	// Verify response mentions the date.
	if !strings.Contains(text, "2026") {
		t.Errorf("expected '2026' in response, got %q", text)
	}

	if terminal == nil || terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %+v", terminal)
	}
	t.Logf("terminal: reason=%s turn=%d", terminal.Reason, terminal.Turn)
}

// =============================================
// Test: Abort 中断真实 API 调用
// =============================================

func TestE2E_RealProvider_Abort(t *testing.T) {
	eng := buildFullEngine(t)

	ctx := context.Background()
	msg := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Write a very long essay about the history of computing, at least 2000 words."}},
	}

	ch, err := eng.ProcessMessage(ctx, "e2e-abort-1", msg)
	if err != nil {
		t.Fatal(err)
	}

	// Wait briefly for streaming to start, then abort.
	time.Sleep(500 * time.Millisecond)
	if err := eng.AbortSession(ctx, "e2e-abort-1"); err != nil {
		t.Fatal(err)
	}

	text, terminal, _ := collect(t, ch, 15*time.Second)
	t.Logf("aborted after text length=%d", len(text))

	if terminal == nil {
		t.Fatal("expected terminal event after abort")
	}
	// Could be aborted_streaming or model_error depending on timing.
	allowed := map[types.TerminalReason]bool{
		types.TerminalAbortedStreaming: true,
		types.TerminalModelError:       true,
		types.TerminalCompleted:        true, // may complete before abort hits
	}
	if !allowed[terminal.Reason] {
		t.Errorf("unexpected terminal reason: %s", terminal.Reason)
	}
	t.Logf("terminal: reason=%s turn=%d msg=%s", terminal.Reason, terminal.Turn, terminal.Message)
}

// =============================================
// Test: 多轮对话 — 同一 session 连续提问
// =============================================

func TestE2E_RealProvider_MultiTurn(t *testing.T) {
	eng := buildFullEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Turn 1: set context.
	msg1 := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Remember this number: 7742"}},
	}
	ch1, err := eng.ProcessMessage(ctx, "e2e-multi-1", msg1)
	if err != nil {
		t.Fatal(err)
	}
	text1, _, _ := collect(t, ch1, 30*time.Second)
	t.Logf("turn 1: %q", text1)

	// Turn 2: ask about the context from turn 1.
	msg2 := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "What number did I ask you to remember? Reply with just the number."}},
	}
	ch2, err := eng.ProcessMessage(ctx, "e2e-multi-1", msg2)
	if err != nil {
		t.Fatal(err)
	}
	text2, terminal2, _ := collect(t, ch2, 30*time.Second)
	t.Logf("turn 2: %q", text2)

	if !strings.Contains(text2, "7742") {
		t.Errorf("expected '7742' in turn 2 response, got %q", text2)
	}
	if terminal2 != nil {
		t.Logf("terminal: reason=%s turn=%d", terminal2.Reason, terminal2.Turn)
	}
}

// Suppress unused import warning for fmt in non-test code.
var _ = fmt.Sprint
