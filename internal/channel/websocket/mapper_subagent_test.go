package websocket

import (
	"encoding/json"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestMapSubAgentStart(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:          types.EngineEventSubAgentStart,
		AgentID:       "agent_abc123",
		AgentName:     "Explore",
		AgentDesc:     "Explores the codebase",
		AgentType:     "explore",
		ParentAgentID: "agent_parent1",
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var msg SubAgentStartMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != MsgTypeSubAgentStart {
		t.Errorf("expected type %s, got %s", MsgTypeSubAgentStart, msg.Type)
	}
	if msg.SessionID != "s1" {
		t.Errorf("expected session_id s1, got %s", msg.SessionID)
	}
	if msg.AgentID != "agent_abc123" {
		t.Errorf("expected agent_id agent_abc123, got %s", msg.AgentID)
	}
	if msg.AgentName != "Explore" {
		t.Errorf("expected agent_name Explore, got %s", msg.AgentName)
	}
	if msg.Description != "Explores the codebase" {
		t.Errorf("expected description 'Explores the codebase', got %s", msg.Description)
	}
	if msg.AgentType != "explore" {
		t.Errorf("expected agent_type explore, got %s", msg.AgentType)
	}
	if msg.ParentAgentID != "agent_parent1" {
		t.Errorf("expected parent_agent_id agent_parent1, got %s", msg.ParentAgentID)
	}
	if msg.EventID == "" {
		t.Error("expected non-empty event_id")
	}
}

func TestMapSubAgentStart_MinimalFields(t *testing.T) {
	m := NewEventMapper("s2", false)
	event := &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_xyz",
		AgentType: "general-purpose",
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var msg SubAgentStartMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.AgentName != "" {
		t.Errorf("expected empty agent_name for minimal event, got %s", msg.AgentName)
	}
	if msg.ParentAgentID != "" {
		t.Errorf("expected empty parent_agent_id for minimal event, got %s", msg.ParentAgentID)
	}
}

func TestMapSubAgentEnd(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:        types.EngineEventSubAgentEnd,
		AgentID:     "agent_abc123",
		AgentName:   "Explore",
		AgentStatus: "completed",
		Duration:    1500,
		Usage: &types.Usage{
			InputTokens:  1000,
			OutputTokens: 500,
			CacheRead:    200,
			CacheWrite:   100,
		},
		Terminal: &types.Terminal{
			Reason: types.TerminalCompleted,
			Turn:   3,
		},
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var msg SubAgentEndMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != MsgTypeSubAgentEnd {
		t.Errorf("expected type %s, got %s", MsgTypeSubAgentEnd, msg.Type)
	}
	if msg.SessionID != "s1" {
		t.Errorf("expected session_id s1, got %s", msg.SessionID)
	}
	if msg.AgentID != "agent_abc123" {
		t.Errorf("expected agent_id agent_abc123, got %s", msg.AgentID)
	}
	if msg.AgentName != "Explore" {
		t.Errorf("expected agent_name Explore, got %s", msg.AgentName)
	}
	if msg.Status != "completed" {
		t.Errorf("expected status completed, got %s", msg.Status)
	}
	if msg.DurationMs != 1500 {
		t.Errorf("expected duration_ms 1500, got %d", msg.DurationMs)
	}
	if msg.NumTurns != 3 {
		t.Errorf("expected num_turns 3, got %d", msg.NumTurns)
	}
	if msg.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if msg.Usage.InputTokens != 1000 {
		t.Errorf("expected input_tokens 1000, got %d", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 500 {
		t.Errorf("expected output_tokens 500, got %d", msg.Usage.OutputTokens)
	}
	if msg.Usage.CacheRead != 200 {
		t.Errorf("expected cache_read_tokens 200, got %d", msg.Usage.CacheRead)
	}
	if msg.Usage.CacheWrite != 100 {
		t.Errorf("expected cache_write_tokens 100, got %d", msg.Usage.CacheWrite)
	}
	if msg.EventID == "" {
		t.Error("expected non-empty event_id")
	}
}

func TestMapSubAgentEnd_NoUsage(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:        types.EngineEventSubAgentEnd,
		AgentID:     "agent_def456",
		AgentStatus: "error",
		Duration:    300,
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var msg SubAgentEndMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Usage != nil {
		t.Error("expected nil usage when event has no usage")
	}
	if msg.NumTurns != 0 {
		t.Errorf("expected num_turns 0 when no terminal, got %d", msg.NumTurns)
	}
	if msg.Status != "error" {
		t.Errorf("expected status error, got %s", msg.Status)
	}
}

func TestMapSubAgentEnd_MaxTurnsStatus(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:        types.EngineEventSubAgentEnd,
		AgentID:     "agent_max",
		AgentStatus: "max_turns",
		Duration:    5000,
		Terminal: &types.Terminal{
			Reason: types.TerminalMaxTurns,
			Turn:   10,
		},
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}

	var msg SubAgentEndMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Status != "max_turns" {
		t.Errorf("expected status max_turns, got %s", msg.Status)
	}
	if msg.NumTurns != 10 {
		t.Errorf("expected num_turns 10, got %d", msg.NumTurns)
	}
}
