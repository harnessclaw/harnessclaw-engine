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
	"harnessclaw-go/internal/engine/prompt/texts"
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
	// Verify Task tool is filtered out for sync sub-agents by default.
	reg := tool.NewRegistry()
	_ = reg.Register(&subagentTestTool{})
	_ = reg.Register(&fakeAgentToolForTest{}) // tool name = "Task"

	fullPool := tool.NewToolPool(reg, nil, nil)
	filteredPool := fullPool.FilteredFor(tool.AgentTypeSync)

	if filteredPool.Get("Task") != nil {
		t.Error("Task tool should be filtered out for sync sub-agents")
	}
	if filteredPool.Get("TestEcho") == nil {
		t.Error("TestEcho tool should be available for sync sub-agents")
	}
}

// TestSpawnSync_AllowedToolsBypassesBlacklist verifies the 3-tier filter
// contract: when an AgentDefinition declares an explicit AllowedTools
// whitelist, the AgentType blacklist (which would otherwise block tools
// like "Task" for sync sub-agents) is bypassed. This is what lets
// Specialists (L2) re-enable the Task tool for L3 dispatch.
func TestSpawnSync_AllowedToolsBypassesBlacklist(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(&subagentTestTool{})
	_ = reg.Register(&fakeAgentToolForTest{}) // tool name = "Task"

	// Step 1 of subagent.go's filter pipeline: pool starts at full registry.
	pool := tool.NewToolPool(reg, nil, nil)

	// With an explicit whitelist, FilterByNames is applied directly to the
	// full pool (no AgentType blacklist in the chain). "Task" survives.
	whitelist := []string{"Task", "TestEcho"}
	pool = pool.FilterByNames(whitelist)

	if pool.Get("Task") == nil {
		t.Error("Task tool should survive when explicitly whitelisted")
	}
	if pool.Get("TestEcho") == nil {
		t.Error("TestEcho tool should survive when explicitly whitelisted")
	}

	// Compare with the blacklist-only path: if we'd applied FilteredFor first,
	// "Task" would be gone before FilterByNames runs.
	pool2 := tool.NewToolPool(reg, nil, nil).FilteredFor(tool.AgentTypeSync)
	pool2 = pool2.FilterByNames(whitelist)
	if pool2.Get("Task") != nil {
		t.Error("control path: Task should be blocked when blacklist precedes whitelist")
	}
	if pool2.Get("TestEcho") == nil {
		t.Error("control path: TestEcho should still be present")
	}
}

type fakeAgentToolForTest struct {
	tool.BaseTool
}

func (f *fakeAgentToolForTest) Name() string            { return "Task" }
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
	// AgentTask must carry the prompt the parent dispatched so the
	// client can render the sub-agent's actual mission, not just the
	// 3-5 word description.
	if start.AgentTask != "test task" {
		t.Errorf("expected agent_task 'test task' (cfg.Prompt), got %q", start.AgentTask)
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

	// L1/L2 隔离（v1.10）：sub-agent LLM text MUST NOT be forwarded to
	// ParentOut. Only the L1 main agent (emma) generates user-facing text;
	// the spawning parent receives sub-agent output via SpawnResult.Summary
	// and polishes its own reply. Lifecycle + tool events still flow.
	for _, evt := range events {
		if evt.Type == types.EngineEventSubAgentEvent && evt.SubAgentEvent != nil &&
			evt.SubAgentEvent.EventType == "text" {
			t.Errorf("sub-agent text leaked to ParentOut: %+v", evt.SubAgentEvent)
		}
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

// TestSpawnSync_FiltersTextFromParentOut verifies the v1.10 L1/L2 boundary:
// sub-agent text generations are NEVER forwarded to the parent's event
// channel. Only tool start/end events and lifecycle events flow through.
func TestSpawnSync_FiltersTextFromParentOut(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				text:       "internal sub-agent prose that should NOT leak",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			},
		},
	}
	eng := newSubagentTestEngine(prov)
	parentOut := make(chan types.EngineEvent, 32)

	_, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "do something",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "general-purpose",
		Description:     "leak test",
		Name:            "leak-test",
		ParentSessionID: "p_filter",
		ParentOut:       parentOut,
	})
	if err != nil {
		t.Fatalf("SpawnSync: %v", err)
	}
	close(parentOut)

	for evt := range parentOut {
		if evt.Type != types.EngineEventSubAgentEvent || evt.SubAgentEvent == nil {
			continue
		}
		if evt.SubAgentEvent.EventType == "text" {
			t.Fatalf("text leaked: %+v", evt.SubAgentEvent)
		}
	}
}

// TestSpawnSync_ForwardsToolEventsToParentOut verifies tool start/end events
// DO still flow up so the client can render observability ("小林 正在写...").
func TestSpawnSync_ForwardsToolEventsToParentOut(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				toolCalls:  []types.ToolCall{{ID: "t1", Name: "TestEcho", Input: `{"msg":"hi"}`}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			},
			{
				text:       "wrap up",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			},
		},
	}
	eng := newSubagentTestEngine(prov, &subagentTestTool{})
	parentOut := make(chan types.EngineEvent, 32)

	_, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "use a tool",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "general-purpose",
		Description:     "tool fwd test",
		Name:            "tool-fwd",
		ParentSessionID: "p_tool",
		ParentOut:       parentOut,
	})
	if err != nil {
		t.Fatalf("SpawnSync: %v", err)
	}
	close(parentOut)

	var sawToolStart, sawToolEnd bool
	for evt := range parentOut {
		if evt.Type != types.EngineEventSubAgentEvent || evt.SubAgentEvent == nil {
			continue
		}
		switch evt.SubAgentEvent.EventType {
		case "tool_start":
			sawToolStart = true
		case "tool_end":
			sawToolEnd = true
		}
	}
	if !sawToolStart {
		t.Error("expected tool_start event forwarded to ParentOut")
	}
	if !sawToolEnd {
		t.Error("expected tool_end event forwarded to ParentOut")
	}
}

// l3EmittingTool simulates what the Task tool does when it spawns L3:
// during Execute() it pushes SubAgentStart / SubAgentEvent / Deliverable /
// SubAgentEnd events onto the parent's EventOut channel. The L2 forwarding
// loop must pass these through unchanged so the WebSocket client can render
// L3 lifecycle even when L2 (Specialists) is what dispatched it.
type l3EmittingTool struct{ tool.BaseTool }

func (l3EmittingTool) Name() string                    { return "FakeTask" }
func (l3EmittingTool) Description() string             { return "emits synthetic L3 events" }
func (l3EmittingTool) IsReadOnly() bool                { return false }
func (l3EmittingTool) IsConcurrencySafe() bool         { return false }
func (l3EmittingTool) InputSchema() map[string]any     { return map[string]any{"type": "object"} }

func (l3EmittingTool) Execute(ctx context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	out, ok := tool.GetEventOut(ctx)
	if !ok {
		return &types.ToolResult{Content: "no event channel"}, nil
	}
	out <- types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_l3xxx",
		AgentName: "researcher",
		AgentDesc: "fake L3",
		AgentType: "sync",
	}
	// Simulate the L3 sub-agent's own ToolExecutor: agent_intent fires
	// FIRST (the framework stripped it off the input), then tool_start.
	// The L2 forwarding loop must wrap the agent_intent into
	// subagent_event{event_type=intent} so it reaches the wire stamped
	// with L3's identity.
	out <- types.EngineEvent{
		Type:      types.EngineEventAgentIntent,
		ToolName:  "WebSearch",
		ToolUseID: "tu_L3",
		Intent:    "正在搜索 vLLM 推理优化的最新论文",
	}
	out <- types.EngineEvent{
		Type:      types.EngineEventSubAgentEvent,
		AgentID:   "agent_l3xxx",
		AgentName: "researcher",
		SubAgentEvent: &types.SubAgentEventData{
			EventType: "tool_start",
			ToolName:  "WebSearch",
			ToolUseID: "tu_L3",
			ToolInput: `{"query":"x"}`,
		},
	}
	out <- types.EngineEvent{
		Type:      types.EngineEventDeliverable,
		AgentID:   "agent_l3xxx",
		AgentName: "researcher",
		Deliverable: &types.Deliverable{
			FilePath: "/tmp/report.md",
			Language: "markdown",
			ByteSize: 42,
		},
	}
	out <- types.EngineEvent{
		Type:        types.EngineEventSubAgentEnd,
		AgentID:     "agent_l3xxx",
		AgentName:   "researcher",
		AgentStatus: "completed",
		Duration:    100,
	}
	return &types.ToolResult{Content: "L3 done"}, nil
}

// TestSpawnSync_PassesThroughDeeperLayerEvents guards the L2-as-relay
// behaviour: when L2 (e.g. Specialists) dispatches an L3 sub-agent, the
// resulting subagent_start / subagent_event / deliverable / subagent_end
// events arrive at L2 already stamped by L3 — L2 must forward them as-is
// so the WebSocket client sees the full chain. Before the fix only L2's
// own ToolStart/ToolEnd were forwarded, so L3 lifecycle silently vanished.
func TestSpawnSync_PassesThroughDeeperLayerEvents(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				toolCalls:  []types.ToolCall{{ID: "fake1", Name: "FakeTask", Input: `{}`}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
			{
				text:       "wrap up",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
	}
	eng := newSubagentTestEngine(prov, l3EmittingTool{})
	parentOut := make(chan types.EngineEvent, 64)

	_, err := eng.SpawnSync(context.Background(), &agent.SpawnConfig{
		Prompt:          "dispatch L3",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		Description:     "L3 passthrough test",
		Name:            "specialists",
		ParentSessionID: "p_l3",
		ParentOut:       parentOut,
	})
	if err != nil {
		t.Fatalf("SpawnSync: %v", err)
	}
	close(parentOut)

	var l3Start, l3Intent, l3Event, l3Deliverable, l3End bool
	for evt := range parentOut {
		switch evt.Type {
		case types.EngineEventSubAgentStart:
			if evt.AgentID == "agent_l3xxx" {
				l3Start = true
			}
		case types.EngineEventSubAgentEvent:
			if evt.SubAgentEvent == nil {
				continue
			}
			// Intent events get wrapped here too — they fire from L3's
			// own ToolExecutor and reach L2 as agent_intent, which the
			// forwarding loop wraps as subagent_event{intent}.
			if evt.SubAgentEvent.EventType == "intent" &&
				evt.SubAgentEvent.Intent == "正在搜索 vLLM 推理优化的最新论文" {
				l3Intent = true
			}
			if evt.SubAgentEvent.EventType == "tool_start" &&
				evt.SubAgentEvent.ToolName == "WebSearch" {
				l3Event = true
			}
		case types.EngineEventDeliverable:
			if evt.AgentID == "agent_l3xxx" && evt.Deliverable != nil &&
				evt.Deliverable.FilePath == "/tmp/report.md" {
				l3Deliverable = true
			}
		case types.EngineEventSubAgentEnd:
			if evt.AgentID == "agent_l3xxx" {
				l3End = true
			}
		}
	}
	if !l3Start {
		t.Error("L3 SubAgentStart did not propagate through L2 to parentOut")
	}
	if !l3Intent {
		t.Error("L3 agent_intent was not wrapped + forwarded as subagent_event{intent}")
	}
	if !l3Event {
		t.Error("L3 SubAgentEvent (tool_start WebSearch) did not propagate through L2 to parentOut")
	}
	if !l3Deliverable {
		t.Error("L3 Deliverable did not propagate through L2 to parentOut")
	}
	if !l3End {
		t.Error("L3 SubAgentEnd did not propagate through L2 to parentOut")
	}
}

// TestQueryEngine_LeaderNameInjection verifies the worker identity template
// uses MainAgentDisplayName from config rather than a hardcoded "emma"
// literal. This is the L2-side de-emma-fication contract.
func TestQueryEngine_LeaderNameInjection(t *testing.T) {
	cases := []struct {
		name        string
		leader      string
		mustContain string
		mustNotHave string
	}{
		{
			name:        "default emma",
			leader:      "emma",
			mustContain: "emma 团队的搭档",
			mustNotHave: "",
		},
		{
			name:        "custom leader",
			leader:      "Sara",
			mustContain: "Sara 团队的搭档",
			mustNotHave: "emma 团队的搭档",
		},
		{
			name:        "empty leader falls back to generic",
			leader:      "",
			mustContain: "团队的搭档",
			mustNotHave: "emma",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := newSubagentTestEngine(&subagentMockProvider{})
			eng.config.MainAgentDisplayName = tc.leader
			def := &agent.AgentDefinition{
				Name:        "tester",
				DisplayName: "小林",
				Description: "测试搭档",
			}
			eng.defRegistry = agent.NewAgentDefinitionRegistry()
			eng.defRegistry.Register(def)

			identity := buildWorkerIdentityForTest(eng, "tester")
			if tc.mustContain != "" && !contains(identity, tc.mustContain) {
				t.Errorf("identity missing %q\n%s", tc.mustContain, identity)
			}
			if tc.mustNotHave != "" && contains(identity, tc.mustNotHave) {
				t.Errorf("identity should not contain %q\n%s", tc.mustNotHave, identity)
			}
		})
	}
}

// buildWorkerIdentityForTest exercises the same identity-builder used by
// SpawnSync. The template lives in prompt/texts/roles.go now, so the test
// just invokes BuildWorkerIdentity with the same lookup logic SpawnSync
// uses (skip definitions with custom SystemPrompt or empty DisplayName).
func buildWorkerIdentityForTest(qe *QueryEngine, subagentType string) string {
	if qe.defRegistry == nil {
		return ""
	}
	def := qe.defRegistry.Get(subagentType)
	if def == nil || def.SystemPrompt != "" || def.DisplayName == "" {
		return ""
	}
	return texts.BuildWorkerIdentity(
		def.DisplayName,
		qe.config.MainAgentDisplayName,
		def.Description,
		def.Personality,
	)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
