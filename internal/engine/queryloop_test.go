package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider/anthropic"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// ==============================
// Config & helpers
// ==============================

type testLLMConfig struct {
	LLM struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"llm"`
}

func loadProvider(t *testing.T) *anthropic.Client {
	t.Helper()
	paths := []string{"../../testdata/llm.yaml", "testdata/llm.yaml"}
	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}
	var cfg testLLMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse llm config: %v", err)
	}
	return anthropic.New(anthropic.Config{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
	})
}

func newTestEngine(t *testing.T, tools ...tool.Tool) (*QueryEngine, *event.Bus) {
	t.Helper()
	prov := loadProvider(t)
	logger, _ := zap.NewDevelopment()
	store := memory.New()
	mgr := session.NewManager(store, logger, 10*time.Minute)
	bus := event.NewBus()
	reg := tool.NewRegistry()
	for _, tl := range tools {
		_ = reg.Register(tl)
	}

	cfg := DefaultQueryEngineConfig()
	cfg.MaxTurns = 10
	cfg.ToolTimeout = 30 * time.Second
	cfg.MaxTokens = 1024
	cfg.SystemPrompt = "You are a helpful assistant. Be concise. Always use tools when asked."
	cfg.ClientTools = false // Use server-side tool execution for these tests.

	eng := NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, nil)
	return eng, bus
}

func userMsg(text string) *types.Message {
	return &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: text}},
	}
}

func drain(t *testing.T, ch <-chan types.EngineEvent, timeout time.Duration) (text string, terminal *types.Terminal, events []types.EngineEvent) {
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
			switch evt.Type {
			case types.EngineEventText:
				text += evt.Text
			case types.EngineEventDone:
				terminal = evt.Terminal
			}
		case <-timer.C:
			t.Fatalf("timeout (%v), collected %d events, text=%q", timeout, len(events), text)
		}
	}
}

// ==============================
// Tool definitions
// ==============================

type echoTool struct {
	tool.BaseTool
	calls atomic.Int32
}

func (et *echoTool) Name() string               { return "echo" }
func (et *echoTool) Description() string         { return "Echoes the input text back. Parameter: text (string)" }
func (et *echoTool) IsReadOnly() bool            { return true }
func (et *echoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "text to echo back"},
		},
		"required": []string{"text"},
	}
}
func (et *echoTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	et.calls.Add(1)
	var p struct{ Text string `json:"text"` }
	json.Unmarshal(input, &p)
	return &types.ToolResult{Content: "echo: " + p.Text}, nil
}

type addTool struct {
	tool.BaseTool
	calls atomic.Int32
}

func (at *addTool) Name() string               { return "add" }
func (at *addTool) Description() string         { return "Adds two numbers. Parameters: a (number), b (number)" }
func (at *addTool) IsReadOnly() bool            { return true }
func (at *addTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "number", "description": "first number"},
			"b": map[string]any{"type": "number", "description": "second number"},
		},
		"required": []string{"a", "b"},
	}
}
func (at *addTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	at.calls.Add(1)
	var p struct {
		A float64 `json:"a"`
		B float64 `json:"b"`
	}
	json.Unmarshal(input, &p)
	return &types.ToolResult{Content: fmt.Sprintf("%g", p.A+p.B)}, nil
}

// ==============================
// Test 1: 纯文本回复
// ==============================

func TestQueryLoop_SimpleTextResponse(t *testing.T) {
	eng, _ := newTestEngine(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-1", userMsg("Say exactly: Hello, world!"))
	if err != nil {
		t.Fatal(err)
	}

	text, terminal, _ := drain(t, ch, 30*time.Second)
	t.Logf("response: %q", text)

	if !strings.Contains(text, "Hello") {
		t.Errorf("expected 'Hello' in response, got %q", text)
	}
	if terminal == nil {
		t.Fatal("expected terminal event")
	}
	if terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", terminal.Reason)
	}
	if terminal.Turn != 1 {
		t.Errorf("expected turn 1, got %d", terminal.Turn)
	}
}

// ==============================
// Test 2: 工具调用回环
// ==============================

func TestQueryLoop_ToolCallCycle(t *testing.T) {
	echo := &echoTool{}
	eng, _ := newTestEngine(t, echo)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-2", userMsg("Use the echo tool with text 'ping'. Then tell me what it returned."))
	if err != nil {
		t.Fatal(err)
	}

	text, terminal, events := drain(t, ch, 60*time.Second)
	t.Logf("response: %q", text)

	if echo.calls.Load() < 1 {
		t.Error("expected echo tool to be called at least once")
	}

	var hasToolStart, hasToolEnd bool
	for _, evt := range events {
		if evt.Type == types.EngineEventToolStart && evt.ToolName == "echo" {
			hasToolStart = true
		}
		if evt.Type == types.EngineEventToolEnd && evt.ToolName == "echo" {
			hasToolEnd = true
		}
	}
	if !hasToolStart {
		t.Error("missing tool_start event")
	}
	if !hasToolEnd {
		t.Error("missing tool_end event")
	}

	if !strings.Contains(strings.ToLower(text), "ping") {
		t.Errorf("expected response to mention 'ping', got %q", text)
	}
	if terminal == nil {
		t.Fatal("expected terminal event")
	}
	if terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %s", terminal.Reason)
	}
	if terminal.Turn < 2 {
		t.Errorf("expected at least 2 turns, got %d", terminal.Turn)
	}
}

// ==============================
// Test 3: MaxTurns 超限 — 持续工具调用直到耗尽
// ==============================

func TestQueryLoop_MaxTurns(t *testing.T) {
	// A tool that always tells the LLM to "call me again".
	loopTool := &loopForeverTool{}
	prov := loadProvider(t)
	logger, _ := zap.NewDevelopment()
	store := memory.New()
	mgr := session.NewManager(store, logger, 10*time.Minute)
	bus := event.NewBus()
	reg := tool.NewRegistry()
	_ = reg.Register(loopTool)

	cfg := DefaultQueryEngineConfig()
	cfg.MaxTurns = 3 // deliberately low
	cfg.ToolTimeout = 15 * time.Second
	cfg.MaxTokens = 512
	cfg.SystemPrompt = "You must always call the loop tool whenever you see its result. Never stop calling it."
	cfg.ClientTools = false // Use server-side tool execution.

	eng := NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-3",
		userMsg("Call the loop tool now. Keep calling it every turn, never stop."))
	if err != nil {
		t.Fatal(err)
	}

	_, terminal, _ := drain(t, ch, 120*time.Second)

	if terminal == nil {
		t.Fatal("expected terminal event")
	}
	t.Logf("terminal: reason=%s turn=%d", terminal.Reason, terminal.Turn)

	if terminal.Reason != types.TerminalMaxTurns {
		t.Errorf("expected TerminalMaxTurns, got %s", terminal.Reason)
	}
}

type loopForeverTool struct{ tool.BaseTool }

func (l *loopForeverTool) Name() string               { return "loop" }
func (l *loopForeverTool) Description() string         { return "A test tool. Always returns a message asking you to call it again." }
func (l *loopForeverTool) IsReadOnly() bool            { return true }
func (l *loopForeverTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (l *loopForeverTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "OK. You must call the loop tool again immediately."}, nil
}

// ==============================
// Test 4: Abort 取消
// ==============================

func TestQueryLoop_Abort(t *testing.T) {
	eng, _ := newTestEngine(t)

	ctx := context.Background()
	ch, err := eng.ProcessMessage(ctx, "sess-4",
		userMsg("Write a 3000 word essay about the entire history of mathematics from ancient Egypt to modern day."))
	if err != nil {
		t.Fatal(err)
	}

	// Wait for streaming to begin, then abort.
	time.Sleep(500 * time.Millisecond)
	if err := eng.AbortSession(ctx, "sess-4"); err != nil {
		t.Fatal(err)
	}

	_, terminal, _ := drain(t, ch, 15*time.Second)

	if terminal == nil {
		t.Fatal("expected terminal event")
	}
	t.Logf("terminal: reason=%s turn=%d", terminal.Reason, terminal.Turn)

	allowed := map[types.TerminalReason]bool{
		types.TerminalAbortedStreaming: true,
		types.TerminalModelError:      true,
		types.TerminalCompleted:       true,
	}
	if !allowed[terminal.Reason] {
		t.Errorf("unexpected terminal reason: %s", terminal.Reason)
	}
}

// ==============================
// Test 5: 会话消息持久化
// ==============================

func TestQueryLoop_SessionMessages(t *testing.T) {
	prov := loadProvider(t)
	logger, _ := zap.NewDevelopment()
	store := memory.New()
	mgr := session.NewManager(store, logger, 10*time.Minute)
	bus := event.NewBus()
	reg := tool.NewRegistry()
	cfg := DefaultQueryEngineConfig()
	cfg.MaxTurns = 5
	cfg.MaxTokens = 512

	eng := NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-5", userMsg("Say hi"))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch, 30*time.Second)

	sess := mgr.Get("sess-5")
	if sess == nil {
		t.Fatal("expected session to exist")
	}

	msgs := sess.GetMessages()
	t.Logf("session has %d messages", len(msgs))

	// user + assistant = 2
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != types.RoleUser {
		t.Errorf("msg[0] should be user, got %s", msgs[0].Role)
	}
	if msgs[1].Role != types.RoleAssistant {
		t.Errorf("msg[1] should be assistant, got %s", msgs[1].Role)
	}
	if msgs[1].Tokens <= 0 {
		t.Errorf("expected assistant tokens > 0, got %d", msgs[1].Tokens)
	}
}

// ==============================
// Test 6: 事件总线通知
// ==============================

func TestQueryLoop_EventBus(t *testing.T) {
	eng, bus := newTestEngine(t)

	var started, completed atomic.Int32
	bus.Subscribe(event.TopicQueryStarted, func(_ event.Event) { started.Add(1) })
	bus.Subscribe(event.TopicQueryCompleted, func(_ event.Event) { completed.Add(1) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-6", userMsg("Say ok"))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch, 30*time.Second)

	if started.Load() != 1 {
		t.Errorf("expected 1 query.started, got %d", started.Load())
	}
	if completed.Load() != 1 {
		t.Errorf("expected 1 query.completed, got %d", completed.Load())
	}
}

// ==============================
// Test 7: 多轮对话 — 同 session 记忆
// ==============================

func TestQueryLoop_MultiTurnMemory(t *testing.T) {
	eng, _ := newTestEngine(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Turn 1
	ch1, err := eng.ProcessMessage(ctx, "sess-7", userMsg("Remember this secret code: XRAY42"))
	if err != nil {
		t.Fatal(err)
	}
	text1, _, _ := drain(t, ch1, 30*time.Second)
	t.Logf("turn 1: %q", text1)

	// Turn 2
	ch2, err := eng.ProcessMessage(ctx, "sess-7", userMsg("What was the secret code I told you? Reply with just the code."))
	if err != nil {
		t.Fatal(err)
	}
	text2, terminal, _ := drain(t, ch2, 30*time.Second)
	t.Logf("turn 2: %q", text2)

	if !strings.Contains(text2, "XRAY42") {
		t.Errorf("expected 'XRAY42' in turn 2, got %q", text2)
	}
	if terminal != nil {
		t.Logf("terminal: reason=%s turn=%d", terminal.Reason, terminal.Turn)
	}
}

// ==============================
// Test 8: 多工具链式调用
// ==============================

func TestQueryLoop_MultiToolChain(t *testing.T) {
	add := &addTool{}
	echo := &echoTool{}
	eng, _ := newTestEngine(t, add, echo)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ch, err := eng.ProcessMessage(ctx, "sess-8",
		userMsg("First use the add tool to compute 10+20. Then use the echo tool with the result as text. Tell me what happened."))
	if err != nil {
		t.Fatal(err)
	}

	text, terminal, _ := drain(t, ch, 90*time.Second)
	t.Logf("response: %q", text)

	if add.calls.Load() < 1 {
		t.Error("expected add tool to be called")
	}
	if echo.calls.Load() < 1 {
		t.Error("expected echo tool to be called")
	}
	if !strings.Contains(text, "30") {
		t.Errorf("expected '30' in response, got %q", text)
	}
	if terminal != nil {
		t.Logf("terminal: reason=%s turn=%d", terminal.Reason, terminal.Turn)
	}
}
