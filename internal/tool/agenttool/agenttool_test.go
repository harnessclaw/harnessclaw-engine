package agenttool

import (
	"context"
	"encoding/json"
	"errors"
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

func TestAgentTool_Name(t *testing.T) {
	tool := New(&mockSpawner{}, zap.NewNop())
	if tool.Name() != "Task" {
		t.Errorf("expected name 'Task', got %q", tool.Name())
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
			input:   `{"prompt":"find files","subagent_type":"Explore","description":"find test files","name":"explorer"}`,
			wantErr: false,
		},
		{
			name:    "missing prompt",
			input:   `{"subagent_type":"Explore"}`,
			wantErr: true,
		},
		{
			name:    "invalid subagent_type",
			input:   `{"prompt":"hello","subagent_type":"invalid"}`,
			wantErr: true,
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

	input := json.RawMessage(`{"prompt":"what is the meaning of life?","subagent_type":"general-purpose","description":"answer question"}`)
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
	if cfg.SubagentType != "general-purpose" {
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
			DeniedTools: []string{"Bash", "Edit"},
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
		{"Explore", "sync"},
		{"explore", "sync"},
		{"Plan", "sync"},
		{"plan", "sync"},
		{"general-purpose", "sync"},
		{"", "sync"},
	}
	for _, tt := range tests {
		got := resolveAgentType(tt.input)
		if string(got) != tt.want {
			t.Errorf("resolveAgentType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
