package specialists

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/pkg/types"
)

// mockSpawner is a test double for agent.AgentSpawner.
type mockSpawner struct {
	result *agent.SpawnResult
	err    error
	calls  []agent.SpawnConfig
}

func (m *mockSpawner) SpawnSync(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	m.calls = append(m.calls, *cfg)
	return m.result, m.err
}

func TestTool_Metadata(t *testing.T) {
	tl := New(&mockSpawner{}, zap.NewNop())
	if tl.Name() != "Specialists" {
		t.Errorf("Name() = %q, want Specialists", tl.Name())
	}
	if !tl.IsLongRunning() {
		t.Error("IsLongRunning should be true (L2 spawns L3 sub-agents)")
	}
	if tl.IsConcurrencySafe() {
		t.Error("IsConcurrencySafe should be false")
	}
	if tl.IsReadOnly() {
		t.Error("IsReadOnly should be false")
	}
}

func TestTool_Schema(t *testing.T) {
	schema := New(&mockSpawner{}, zap.NewNop()).InputSchema()
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v", schema["type"])
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "task" {
		t.Errorf("required = %v, want [task]", required)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"task", "description"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing property %q", key)
		}
	}
}

func TestTool_Validate(t *testing.T) {
	tl := New(&mockSpawner{}, zap.NewNop())
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"valid minimal", `{"task":"写一封商务邮件"}`, ""},
		{"valid full", `{"task":"做竞品分析","description":"comp analysis"}`, ""},
		{"missing task", `{}`, "task is required"},
		{"blank task", `{"task":"   "}`, "task is required"},
		{"invalid JSON", `not json`, "invalid Specialists input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tl.ValidateInput(json.RawMessage(tc.input))
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestTool_PermissionAutoAllow(t *testing.T) {
	tl := New(&mockSpawner{}, zap.NewNop())
	res := tl.CheckPermission(context.Background(), json.RawMessage(`{"task":"x"}`))
	if res.Behavior != "allow" {
		t.Errorf("Behavior = %q, want allow", res.Behavior)
	}
}

func TestTool_Execute_Success(t *testing.T) {
	sp := &mockSpawner{
		result: &agent.SpawnResult{
			AgentID:   "agent-spec",
			SessionID: "sess-spec",
			Output:    "<summary>报告已完成</summary>\n\n详细内容...",
			Summary:   "报告已完成",
			Status:    "completed",
			Terminal:  &types.Terminal{Reason: types.TerminalCompleted, Turn: 5},
			Usage:     &types.Usage{InputTokens: 1000, OutputTokens: 500},
			NumTurns:  5,
		},
	}
	tl := New(sp, zap.NewNop())

	res, err := tl.Execute(context.Background(),
		json.RawMessage(`{"task":"写竞品分析报告"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "报告已完成") {
		t.Errorf("output should contain summary, got %q", res.Content)
	}
	if len(sp.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(sp.calls))
	}
	call := sp.calls[0]
	if call.SubagentType != SubagentType {
		t.Errorf("SubagentType = %q, want %q", call.SubagentType, SubagentType)
	}
	if call.Prompt != "写竞品分析报告" {
		t.Errorf("Prompt = %q", call.Prompt)
	}
}

func TestTool_Execute_Failure(t *testing.T) {
	sp := &mockSpawner{err: errors.New("spawn failed")}
	tl := New(sp, zap.NewNop())

	res, err := tl.Execute(context.Background(),
		json.RawMessage(`{"task":"x"}`))
	if err != nil {
		t.Fatalf("Execute should not return error, got %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true on spawn failure")
	}
	if !strings.Contains(res.Content, "Specialists execution failed") {
		t.Errorf("error content unexpected: %q", res.Content)
	}
}

func TestTool_Execute_TerminalErrorMarksIsError(t *testing.T) {
	sp := &mockSpawner{
		result: &agent.SpawnResult{
			AgentID:  "a",
			Output:   "partial output",
			Status:   "error",
			Terminal: &types.Terminal{Reason: types.TerminalModelError, Turn: 2},
		},
	}
	tl := New(sp, zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if !res.IsError {
		t.Error("model_error terminal should set IsError=true")
	}
}

func TestTool_Execute_DeliverablesInMetadata(t *testing.T) {
	sp := &mockSpawner{
		result: &agent.SpawnResult{
			AgentID: "a",
			Output:  "<summary>done</summary>",
			Status:  "completed",
			Deliverables: []types.Deliverable{
				{FilePath: "/tmp/report.md"},
				{FilePath: "/tmp/data.csv"},
			},
		},
	}
	tl := New(sp, zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if got, _ := res.Metadata["has_deliverables"].(bool); !got {
		t.Error("metadata should flag has_deliverables")
	}
	if dels, _ := res.Metadata["deliverables"].([]types.Deliverable); len(dels) != 2 {
		t.Errorf("deliverables in metadata = %d, want 2", len(dels))
	}
}

func TestTool_InvalidInputReturnsErrorResult(t *testing.T) {
	tl := New(&mockSpawner{}, zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError {
		t.Error("blank task should produce IsError=true")
	}
	if !strings.Contains(res.Content, "task is required") {
		t.Errorf("error content unexpected: %q", res.Content)
	}
}
