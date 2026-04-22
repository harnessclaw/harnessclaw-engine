package teamtool

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
)

func TestCreateTool_Name(t *testing.T) {
	tm := agent.NewTeamManager()
	broker := agent.NewMessageBroker()
	tool := NewCreate(tm, broker, zap.NewNop())
	if tool.Name() != "TeamCreate" {
		t.Errorf("expected 'TeamCreate', got %q", tool.Name())
	}
}

func TestCreateTool_Execute_Success(t *testing.T) {
	tm := agent.NewTeamManager()
	broker := agent.NewMessageBroker()
	tool := NewCreate(tm, broker, zap.NewNop())

	input := json.RawMessage(`{"team_name":"my-project","description":"Test team"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// Verify team was created
	team := tm.Get("my-project")
	if team == nil {
		t.Fatal("expected team to be created")
	}
	if team.Description != "Test team" {
		t.Errorf("expected description 'Test team', got %q", team.Description)
	}
}

func TestCreateTool_Execute_Duplicate(t *testing.T) {
	tm := agent.NewTeamManager()
	broker := agent.NewMessageBroker()
	tool := NewCreate(tm, broker, zap.NewNop())

	input := json.RawMessage(`{"team_name":"proj"}`)
	_, _ = tool.Execute(context.Background(), input)

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for duplicate team")
	}
}

func TestCreateTool_ValidateInput(t *testing.T) {
	tm := agent.NewTeamManager()
	broker := agent.NewMessageBroker()
	tool := NewCreate(tm, broker, zap.NewNop())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", `{"team_name":"proj"}`, false},
		{"missing name", `{}`, true},
		{"empty name", `{"team_name":""}`, true},
		{"invalid json", `not json`, true},
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

func TestDeleteTool_Name(t *testing.T) {
	tm := agent.NewTeamManager()
	tool := NewDelete(tm, zap.NewNop())
	if tool.Name() != "TeamDelete" {
		t.Errorf("expected 'TeamDelete', got %q", tool.Name())
	}
}

func TestDeleteTool_Execute_Success(t *testing.T) {
	tm := agent.NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")

	tool := NewDelete(tm, zap.NewNop())
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"proj"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}

	if tm.Get("proj") != nil {
		t.Error("expected team to be deleted")
	}
}

func TestDeleteTool_Execute_NoContext(t *testing.T) {
	tm := agent.NewTeamManager()
	tool := NewDelete(tm, zap.NewNop())

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when no team_name")
	}
}

func TestDeleteTool_Execute_ActiveMembers(t *testing.T) {
	tm := agent.NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_ = tm.AddMember("proj", agent.TeamMember{Name: "worker", AgentID: "a1", Status: "active"})

	tool := NewDelete(tm, zap.NewNop())
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"proj"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when team has active members")
	}
}
