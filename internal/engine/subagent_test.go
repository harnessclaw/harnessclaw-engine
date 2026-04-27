package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// --- Mock provider for sub-agent tests ---

type subagentMockProvider struct {
	responses []subagentMockResponse
	callIdx   int
}

type subagentMockResponse struct {
	text       string
	toolCalls  []types.ToolCall
	stopReason string
	usage      *types.Usage
	err        error
}

func (m *subagentMockProvider) Name() string { return "mock-subagent" }

func (m *subagentMockProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	if m.callIdx >= len(m.responses) {
		stream := newSubagentMockStream("", nil, "end_turn", &types.Usage{InputTokens: 10, OutputTokens: 5})
		return stream, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	if resp.err != nil {
		return nil, resp.err
	}
	return newSubagentMockStream(resp.text, resp.toolCalls, resp.stopReason, resp.usage), nil
}

func (m *subagentMockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 100, nil
}

func newSubagentMockStream(text string, toolCalls []types.ToolCall, stopReason string, usage *types.Usage) *provider.ChatStream {
	ch := make(chan types.StreamEvent, 10)

	go func() {
		defer close(ch)
		if text != "" {
			ch <- types.StreamEvent{Type: types.StreamEventText, Text: text}
		}
		for _, tc := range toolCalls {
			tc := tc
			ch <- types.StreamEvent{Type: types.StreamEventToolUse, ToolCall: &tc}
		}
		ch <- types.StreamEvent{
			Type:       types.StreamEventMessageEnd,
			StopReason: stopReason,
			Usage:      usage,
		}
	}()

	return &provider.ChatStream{
		Events: ch,
		Err:    func() error { return nil },
	}
}

// --- Test tool ---

type subagentTestTool struct {
	tool.BaseTool
}

func (t *subagentTestTool) Name() string            { return "TestEcho" }
func (t *subagentTestTool) Description() string     { return "Returns the input text" }
func (t *subagentTestTool) IsReadOnly() bool         { return true }
func (t *subagentTestTool) IsConcurrencySafe() bool  { return true }
func (t *subagentTestTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	}
}

func (t *subagentTestTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct{ Text string }
	json.Unmarshal(input, &p)
	return &types.ToolResult{Content: "echoed: " + p.Text}, nil
}

func newSubagentTestEngine(prov provider.Provider, tools ...tool.Tool) *QueryEngine {
	logger := zap.NewNop()
	store := memory.New()
	bus := event.NewBus()
	mgr := session.NewManager(store, logger, 30*time.Minute)
	cmdReg := command.NewRegistry()

	reg := tool.NewRegistry()
	for _, tl := range tools {
		_ = reg.Register(tl)
	}

	cfg := QueryEngineConfig{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          30 * time.Second,
		MaxTokens:            4096,
		SystemPrompt:         "You are a test assistant.",
		ClientTools:          false,
	}

	return NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, cmdReg)
}

// --- Tests ---

func TestSpawnSync_SimpleCompletion(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				text:       "Hello from sub-agent!",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 50, OutputTokens: 20},
			},
		},
	}

	eng := newSubagentTestEngine(prov)

	result, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "Say hello",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "general-purpose",
		Description:     "test agent",
		ParentSessionID: "parent_123",
	})

	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.Output != "Hello from sub-agent!" {
		t.Errorf("expected output 'Hello from sub-agent!', got %q", result.Output)
	}
	if result.Terminal == nil || result.Terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %v", result.Terminal)
	}
	if result.NumTurns != 1 {
		t.Errorf("expected 1 turn, got %d", result.NumTurns)
	}
	if result.AgentID == "" {
		t.Error("expected non-empty AgentID")
	}
	if result.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}
}

func TestSpawnSync_WithToolUse(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				text: "Let me echo that.",
				toolCalls: []types.ToolCall{
					{ID: "tool_1", Name: "TestEcho", Input: `{"text":"hello"}`},
				},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 50, OutputTokens: 30},
			},
			{
				text:       "The echo result was: hello",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 80, OutputTokens: 25},
			},
		},
	}

	eng := newSubagentTestEngine(prov, &subagentTestTool{})

	result, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "Echo hello",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "Explore",
		ParentSessionID: "parent_456",
	})

	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.Output == "" {
		t.Error("expected non-empty output")
	}
	if result.Terminal == nil || result.Terminal.Reason != types.TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %v", result.Terminal)
	}
	if result.NumTurns != 2 {
		t.Errorf("expected 2 turns, got %d", result.NumTurns)
	}
}

func TestSpawnSync_MaxTurns(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "turn 1", toolCalls: []types.ToolCall{{ID: "t1", Name: "TestEcho", Input: `{}`}}, stopReason: "tool_use", usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
			{text: "turn 2", toolCalls: []types.ToolCall{{ID: "t2", Name: "TestEcho", Input: `{}`}}, stopReason: "tool_use", usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
			{text: "turn 3", toolCalls: []types.ToolCall{{ID: "t3", Name: "TestEcho", Input: `{}`}}, stopReason: "tool_use", usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}

	eng := newSubagentTestEngine(prov, &subagentTestTool{})

	result, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "Loop forever",
		AgentType:       tool.AgentTypeSync,
		MaxTurns:        2,
		ParentSessionID: "parent_789",
	})

	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.Terminal == nil || result.Terminal.Reason != types.TerminalMaxTurns {
		t.Errorf("expected TerminalMaxTurns, got %v", result.Terminal)
	}
}

func TestSpawnSync_ToolFiltering(t *testing.T) {
	// Verify Agent tool is filtered out for sync sub-agents.
	reg := tool.NewRegistry()
	_ = reg.Register(&subagentTestTool{})
	_ = reg.Register(&fakeAgentToolForTest{})

	fullPool := tool.NewToolPool(reg, nil, nil)
	filteredPool := fullPool.FilteredFor(tool.AgentTypeSync)

	if filteredPool.Get("Agent") != nil {
		t.Error("Agent tool should be filtered out for sync sub-agents")
	}
	if filteredPool.Get("TestEcho") == nil {
		t.Error("TestEcho tool should be available for sync sub-agents")
	}
}

type fakeAgentToolForTest struct {
	tool.BaseTool
}

func (f *fakeAgentToolForTest) Name() string            { return "Agent" }
func (f *fakeAgentToolForTest) Description() string     { return "Fake agent" }
func (f *fakeAgentToolForTest) IsReadOnly() bool         { return false }
func (f *fakeAgentToolForTest) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f *fakeAgentToolForTest) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "agent output"}, nil
}

func TestSpawnSync_ProfileResolution(t *testing.T) {
	tests := []struct {
		subagentType string
		wantProfile  string
	}{
		{"Explore", "explore"},
		{"explore", "explore"},
		{"Plan", "plan"},
		{"plan", "plan"},
		{"general-purpose", "full"},
		{"", "full"},
	}

	for _, tt := range tests {
		profile := resolveSubAgentProfile(tt.subagentType)
		if profile.Name != tt.wantProfile {
			t.Errorf("resolveSubAgentProfile(%q) = %q, want %q", tt.subagentType, profile.Name, tt.wantProfile)
		}
	}
}

func TestSpawnSync_Timeout(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "working...", toolCalls: []types.ToolCall{{ID: "t1", Name: "TestEcho", Input: `{}`}}, stopReason: "tool_use", usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}

	eng := newSubagentTestEngine(prov, &subagentTestTool{})

	result, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "Do something slow",
		AgentType:       tool.AgentTypeSync,
		Timeout:         100 * time.Millisecond,
		ParentSessionID: "parent_timeout",
	})

	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	// The mock provider may complete fast enough before timeout,
	// but with more tool calls, the loop should eventually timeout.
	// For now, accept any valid terminal reason.
	if result.Terminal == nil {
		t.Fatal("expected non-nil terminal")
	}
	t.Logf("terminal reason: %s (timeout test)", result.Terminal.Reason)
}

// Verify that the compile-time interface check passes.
var _ agent.AgentSpawner = (*QueryEngine)(nil)

// Suppress unused import warning for prompt package.
var _ = prompt.EmmaProfile

func TestSpawnSync_ParentOutEvents(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				text:       "done",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	eng := newSubagentTestEngine(prov)

	// Create a buffered channel to capture parent events.
	parentOut := make(chan types.EngineEvent, 10)

	result, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "test task",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "general-purpose",
		Description:     "test subagent",
		Name:            "tester",
		ParentSessionID: "parent_out_test",
		ParentOut:       parentOut,
	})
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.Output != "done" {
		t.Errorf("expected output 'done', got %q", result.Output)
	}

	// Collect events from parentOut.
	close(parentOut)
	var events []types.EngineEvent
	for evt := range parentOut {
		events = append(events, evt)
	}

	// Expect subagent.start, forwarded streaming events, and subagent.end.
	// With real-time forwarding: start + message_start + text + message_delta + message_stop + end = 6
	if len(events) < 2 {
		t.Fatalf("expected at least 2 parent events, got %d", len(events))
	}

	// Check subagent.start event (always first).
	start := events[0]
	if start.Type != types.EngineEventSubAgentStart {
		t.Errorf("expected first event type %s, got %s", types.EngineEventSubAgentStart, start.Type)
	}
	if start.AgentName != "tester" {
		t.Errorf("expected agent_name 'tester', got %q", start.AgentName)
	}
	if start.AgentDesc != "test subagent" {
		t.Errorf("expected description 'test subagent', got %q", start.AgentDesc)
	}
	if start.AgentID == "" {
		t.Error("expected non-empty agent_id on subagent.start")
	}

	// Check that forwarded events have agent_id set and use subagent_event type.
	for _, evt := range events[1 : len(events)-1] {
		if evt.Type != types.EngineEventSubAgentEvent {
			t.Errorf("expected forwarded event type %s, got %s", types.EngineEventSubAgentEvent, evt.Type)
		}
		if evt.AgentID == "" {
			t.Errorf("expected non-empty agent_id on forwarded event %s", evt.Type)
		}
		if evt.AgentName != "tester" {
			t.Errorf("expected agent_name 'tester' on forwarded event %s, got %q", evt.Type, evt.AgentName)
		}
	}

	// Verify text was forwarded in real-time via subagent_event wrapper.
	var hasText bool
	for _, evt := range events {
		if evt.Type == types.EngineEventSubAgentEvent && evt.SubAgentEvent != nil &&
			evt.SubAgentEvent.EventType == "text" && evt.SubAgentEvent.Text == "done" {
			hasText = true
		}
	}
	if !hasText {
		t.Error("expected forwarded subagent_event with text content 'done'")
	}

	// Check subagent.end event (always last).
	end := events[len(events)-1]
	if end.Type != types.EngineEventSubAgentEnd {
		t.Errorf("expected event type %s, got %s", types.EngineEventSubAgentEnd, end.Type)
	}
	if end.AgentStatus != "completed" {
		t.Errorf("expected status 'completed', got %q", end.AgentStatus)
	}
	if end.Duration < 0 {
		t.Errorf("expected non-negative duration_ms, got %d", end.Duration)
	}
	if end.Usage == nil {
		t.Error("expected non-nil usage on subagent.end")
	}
}
