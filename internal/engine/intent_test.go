package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// intentTestTool captures whatever input ToolExecutor passes through after
// stripping `intent`. The executor must pass clean JSON without `intent`.
type intentTestTool struct {
	tool.BaseTool
	got json.RawMessage
}

func (t *intentTestTool) Name() string                                    { return "TestTool" }
func (t *intentTestTool) Description() string                             { return "captures input" }
func (t *intentTestTool) IsReadOnly() bool                                 { return true }
func (t *intentTestTool) IsConcurrencySafe() bool                          { return true }
func (t *intentTestTool) InputSchema() map[string]any                      { return map[string]any{"type": "object"} }
func (t *intentTestTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	t.got = append([]byte(nil), raw...)
	return &types.ToolResult{Content: "ok"}, nil
}

// TestStripIntent_ParsesObjectAndRemovesIntent guards the executor's
// extract/strip step. ToolPool.Schemas() forces every tool input to carry
// an `intent` field; the executor must lift it into a progress event AND
// remove it before invoking the tool — otherwise the tool's own validator
// would either reject the unexpected field or accidentally consume it.
func TestStripIntent_ParsesObjectAndRemovesIntent(t *testing.T) {
	cleaned, intent := stripIntent(`{"file_path":"/x","intent":"读取入口文件 main.go"}`)
	if intent != "读取入口文件 main.go" {
		t.Errorf("intent: got %q", intent)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cleaned), &m); err != nil {
		t.Fatalf("cleaned input not valid JSON: %v\n%s", err, cleaned)
	}
	if _, has := m["intent"]; has {
		t.Errorf("intent leaked into cleaned input: %s", cleaned)
	}
	if m["file_path"] != "/x" {
		t.Errorf("non-intent fields lost: %v", m)
	}
}

// TestStripIntent_TolerantOfMissingIntent — if the model didn't fill
// intent (provider relaxed validation), we degrade silently rather than
// crash. The tool still runs and the user just doesn't see a progress
// sentence for that call.
func TestStripIntent_TolerantOfMissingIntent(t *testing.T) {
	cleaned, intent := stripIntent(`{"file_path":"/x"}`)
	if intent != "" {
		t.Errorf("intent should be empty, got %q", intent)
	}
	if cleaned != `{"file_path":"/x"}` {
		t.Errorf("input should be unchanged, got %s", cleaned)
	}
}

// TestStripIntent_NotAnObject — array/scalar inputs aren't valid for
// schema-validated tools, but we must not crash even if a malformed call
// reaches us. Pass through, no progress event.
func TestStripIntent_NotAnObject(t *testing.T) {
	cleaned, intent := stripIntent(`["not", "an", "object"]`)
	if intent != "" || cleaned != `["not", "an", "object"]` {
		t.Errorf("array input mishandled: cleaned=%q intent=%q", cleaned, intent)
	}
}

// TestExecutor_EmitsAgentIntentBeforeToolStart asserts the wire ordering
// the client relies on: agent_intent fires *first*, so a UI showing
// "researcher 正在搜 vLLM" can render before the tool.start payload
// arrives. Also asserts the underlying tool received the input WITHOUT
// the `intent` field — the framework owns the lifecycle, not the tool.
func TestExecutor_EmitsAgentIntentBeforeToolStart(t *testing.T) {
	captureTool := &intentTestTool{}
	reg := tool.NewRegistry()
	if err := reg.Register(captureTool); err != nil {
		t.Fatal(err)
	}
	pool := tool.NewToolPool(reg, nil, nil)

	te := NewToolExecutor(pool, permission.BypassChecker{}, zap.NewNop(), 5*time.Second, nil)

	out := make(chan types.EngineEvent, 16)
	tc := types.ToolCall{
		ID:    "tu_1",
		Name:  "TestTool",
		Input: `{"target":"main.go","intent":"读取 main.go 找入口"}`,
	}

	done := make(chan struct{})
	var result types.ToolResult
	go func() {
		defer close(done)
		result = te.executeSingle(context.Background(), tc, out)
	}()
	<-done
	close(out)

	var events []types.EngineEvent
	for evt := range out {
		events = append(events, evt)
	}

	// Ordering: agent_intent FIRST, tool_start SECOND, tool_end LAST.
	wantOrder := []types.EngineEventType{
		types.EngineEventAgentIntent,
		types.EngineEventToolStart,
		types.EngineEventToolEnd,
	}
	if len(events) != len(wantOrder) {
		t.Fatalf("event count: got %d, want %d\nevents: %+v", len(events), len(wantOrder), events)
	}
	for i, want := range wantOrder {
		if events[i].Type != want {
			t.Errorf("event[%d] type: got %s, want %s", i, events[i].Type, want)
		}
	}

	// agent_intent payload check.
	intentEvt := events[0]
	if intentEvt.Intent != "读取 main.go 找入口" {
		t.Errorf("intent text: got %q", intentEvt.Intent)
	}
	if intentEvt.ToolUseID != "tu_1" || intentEvt.ToolName != "TestTool" {
		t.Errorf("intent attribution lost: %+v", intentEvt)
	}

	// tool_start payload must NOT carry the intent in ToolInput.
	startEvt := events[1]
	if !json.Valid([]byte(startEvt.ToolInput)) {
		t.Fatalf("tool_start.tool_input not valid JSON: %s", startEvt.ToolInput)
	}
	var startInput map[string]any
	_ = json.Unmarshal([]byte(startEvt.ToolInput), &startInput)
	if _, has := startInput["intent"]; has {
		t.Errorf("intent leaked into tool_start.tool_input: %s", startEvt.ToolInput)
	}
	if startInput["target"] != "main.go" {
		t.Errorf("non-intent field lost from tool_start: %v", startInput)
	}

	// The actual tool also got the cleaned input.
	var toolGot map[string]any
	_ = json.Unmarshal(captureTool.got, &toolGot)
	if _, has := toolGot["intent"]; has {
		t.Errorf("intent leaked into the tool's Execute(): %s", captureTool.got)
	}
	if toolGot["target"] != "main.go" {
		t.Errorf("tool didn't receive original fields: %v", toolGot)
	}

	if result.Content != "ok" {
		t.Errorf("tool result lost: %+v", result)
	}
}

// TestExecutor_NoIntent_StillRuns guards graceful degradation: a malformed
// call missing intent should NOT block the tool from running — silence
// beats a hard fail on the user-visible code path.
func TestExecutor_NoIntent_StillRuns(t *testing.T) {
	captureTool := &intentTestTool{}
	reg := tool.NewRegistry()
	_ = reg.Register(captureTool)
	pool := tool.NewToolPool(reg, nil, nil)
	te := NewToolExecutor(pool, permission.BypassChecker{}, zap.NewNop(), 5*time.Second, nil)

	out := make(chan types.EngineEvent, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = te.executeSingle(context.Background(), types.ToolCall{
			ID: "tu_2", Name: "TestTool", Input: `{"target":"x"}`,
		}, out)
	}()
	<-done
	close(out)

	var sawIntent bool
	var count int
	for evt := range out {
		count++
		if evt.Type == types.EngineEventAgentIntent {
			sawIntent = true
		}
	}
	if sawIntent {
		t.Error("agent_intent emitted even though model didn't supply it")
	}
	if count != 2 {
		t.Errorf("expected 2 events (tool_start + tool_end), got %d", count)
	}
}
