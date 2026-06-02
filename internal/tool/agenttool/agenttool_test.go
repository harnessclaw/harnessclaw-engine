package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/sessionstats"
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

func TestAgentTool_Name(t *testing.T) {
	tool := New(&mockSpawner{}, zap.NewNop())
	if tool.Name() != "freelance" {
		t.Errorf("expected name 'coordinator', got %q", tool.Name())
	}
}

func TestAgentTool_IsLongRunning(t *testing.T) {
	tool := New(&mockSpawner{}, zap.NewNop())
	if !tool.IsLongRunning() {
		t.Error("expected IsLongRunning to return true")
	}
}

func TestAgentTool_IsReadOnly(t *testing.T) {
	tool := New(&mockSpawner{}, zap.NewNop())
	if tool.IsReadOnly() {
		t.Error("expected IsReadOnly to return false")
	}
}

func TestAgentTool_ValidateInput(t *testing.T) {
	tool := New(&mockSpawner{}, zap.NewNop())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid minimal input",
			input:   `{"prompt":"search for tests"}`,
			wantErr: false,
		},
		{
			name:    "valid full input",
			input:   `{"prompt":"find files","subagent_type":"plan","description":"find test files","name":"explorer"}`,
			wantErr: false,
		},
		{
			name:    "missing prompt",
			input:   `{"subagent_type":"plan"}`,
			wantErr: true,
		},
		{
			// subagent_type validation was removed in Phase 8: arbitrary names
			// are accepted at the input layer and resolved by defRegistry at
			// spawn time (see engine/subagent.go). "invalid" is still a valid
			// input — the registry falls back to AgentTypeSync default.
			name:    "unknown subagent_type accepted (registry resolves at spawn)",
			input:   `{"prompt":"hello","subagent_type":"invalid"}`,
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "empty prompt",
			input:   `{"prompt":""}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.ValidateInput(json.RawMessage(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInput() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestAgentTool_Execute_Success(t *testing.T) {
	spawner := &mockSpawner{
		result: &agent.SpawnResult{
			Output:    "The answer is 42.",
			Terminal:  &types.Terminal{Reason: types.TerminalCompleted, Turn: 3},
			Usage:     &types.Usage{InputTokens: 100, OutputTokens: 50},
			SessionID: "sess_123",
			AgentID:   "agent_abc",
			NumTurns:  3,
		},
	}
	tool := New(spawner, zap.NewNop())

	input := json.RawMessage(`{"prompt":"what is the meaning of life?","subagent_type":"freelancer","description":"answer question"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Content != "The answer is 42." {
		t.Errorf("expected output 'The answer is 42.', got %q", result.Content)
	}
	if result.IsError {
		t.Error("expected IsError=false")
	}
	if result.Metadata["agent_id"] != "agent_abc" {
		t.Errorf("expected metadata agent_id='agent_abc', got %v", result.Metadata["agent_id"])
	}
	if result.Metadata["num_turns"] != 3 {
		t.Errorf("expected num_turns=3, got %v", result.Metadata["num_turns"])
	}

	// Verify spawn config was passed correctly.
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	cfg := spawner.calls[0]
	if cfg.Prompt != "what is the meaning of life?" {
		t.Errorf("unexpected prompt: %q", cfg.Prompt)
	}
	if cfg.SubagentType != "freelancer" {
		t.Errorf("unexpected subagent_type: %q", cfg.SubagentType)
	}
	if cfg.Description != "answer question" {
		t.Errorf("unexpected description: %q", cfg.Description)
	}
}

func TestAgentTool_Execute_SpawnerError(t *testing.T) {
	spawner := &mockSpawner{
		err: errors.New("provider unavailable"),
	}
	tool := New(spawner, zap.NewNop())

	input := json.RawMessage(`{"prompt":"do something"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}

	if !result.IsError {
		t.Error("expected IsError=true when spawner fails")
	}
	if result.Content == "" {
		t.Error("expected error content to be non-empty")
	}
}

func TestAgentTool_Execute_ModelError(t *testing.T) {
	spawner := &mockSpawner{
		result: &agent.SpawnResult{
			Output:   "partial output",
			Terminal: &types.Terminal{Reason: types.TerminalModelError, Turn: 1},
			Usage:    &types.Usage{InputTokens: 50, OutputTokens: 10},
			AgentID:  "agent_err",
			NumTurns: 1,
		},
	}
	tool := New(spawner, zap.NewNop())

	input := json.RawMessage(`{"prompt":"do something complex"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected IsError=true for model error terminal")
	}
	if result.Metadata["terminal_reason"] != "model_error" {
		t.Errorf("expected terminal_reason=model_error, got %v", result.Metadata["terminal_reason"])
	}
}

func TestAgentTool_Execute_DeniedTools(t *testing.T) {
	spawner := &mockSpawner{
		result: &agent.SpawnResult{
			Output:      "done with limitations",
			Terminal:    &types.Terminal{Reason: types.TerminalCompleted, Turn: 2},
			Usage:       &types.Usage{},
			AgentID:     "agent_d",
			DeniedTools: []string{"bash", "edit"},
			NumTurns:    2,
		},
	}
	tool := New(spawner, zap.NewNop())

	input := json.RawMessage(`{"prompt":"try to edit files"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Error("expected IsError=false for completed terminal")
	}
	denied, ok := result.Metadata["denied_tools"]
	if !ok {
		t.Fatal("expected denied_tools in metadata")
	}
	deniedSlice, ok := denied.([]string)
	if !ok || len(deniedSlice) != 2 {
		t.Errorf("expected 2 denied tools, got %v", denied)
	}
}

func TestResolveAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"explore", "sync"},
		{"plan", "sync"},
		{"plan", "sync"},
		{"freelancer", "sync"},
		{"", "sync"},
	}
	for _, tt := range tests {
		got := resolveAgentType(tt.input)
		if string(got) != tt.want {
			t.Errorf("resolveAgentType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestAgentTool_Execute_PropagatesParentAgentID is the hierarchy regression
// guard. The dispatching agent's session id (== card id in the wire
// envelope) must land in SpawnConfig.ParentAgentID so the translator's
// parentForSubAgent nests the L3 freelancer card under the L2 scheduler
// card. Pre-fix the field was left empty, the translator fell through
// to "most recent tool card" (emma's scheduler tool call), and L3
// rendered as a sibling of L2 in the UI.
func TestAgentTool_Execute_PropagatesParentAgentID(t *testing.T) {
	spawner := &mockSpawner{result: &agent.SpawnResult{
		Output:   "ok",
		Terminal: &types.Terminal{Reason: types.TerminalCompleted},
	}}
	tool := New(spawner, zap.NewNop())

	// Simulate the call site: L2 scheduler module sets its own sess.ID
	// onto ctx via common.WithSubAgentStats → sessionstats.WithSessionID.
	// When L2's LLM calls the freelance tool, that ctx flows through
	// toolexec into agenttool.Execute.
	ctx := sessionstats.WithSessionID(context.Background(), "L2_sess_xyz")

	input := json.RawMessage(`{"prompt":"do work","subagent_type":"freelancer","description":"d"}`)
	_, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
	}
	if got := spawner.calls[0].ParentAgentID; got != "L2_sess_xyz" {
		t.Errorf("ParentAgentID = %q, want L2_sess_xyz", got)
	}
}

// TestAgentTool_Execute_NoSessionInCtxKeepsParentAgentIDEmpty preserves
// the legacy behaviour for call sites that haven't wired sessionstats
// onto ctx (e.g. ad-hoc tests, smoke clients). Empty ParentAgentID
// triggers the translator's "most recent tool card" fallback, which is
// the correct behaviour when we genuinely don't know the parent.
func TestAgentTool_Execute_NoSessionInCtxKeepsParentAgentIDEmpty(t *testing.T) {
	spawner := &mockSpawner{result: &agent.SpawnResult{
		Output:   "ok",
		Terminal: &types.Terminal{Reason: types.TerminalCompleted},
	}}
	tool := New(spawner, zap.NewNop())

	input := json.RawMessage(`{"prompt":"do work","subagent_type":"freelancer","description":"d"}`)
	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if got := spawner.calls[0].ParentAgentID; got != "" {
		t.Errorf("ParentAgentID = %q, want empty (no session in ctx)", got)
	}
}
