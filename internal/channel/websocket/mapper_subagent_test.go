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

// TestMapSubAgentStart_TaskTextPropagates guards the task-visibility fix:
// the full prompt the parent dispatched (EngineEvent.AgentTask) must reach
// the wire as `task` so the client can show "researcher 接到的任务：…".
// Without it, only the 3-5-word AgentDesc gets through and the sub-agent's
// real mission stays invisible.
func TestMapSubAgentStart_TaskTextPropagates(t *testing.T) {
	m := NewEventMapper("s_task", false)
	event := &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_research_1",
		AgentName: "researcher",
		AgentDesc: "调研 LLM 推理",
		AgentTask: "调研大模型推理优化的最新进展，重点关注 vLLM、SGLang、KV-cache 优化方向，整理一份给老板的简报",
		AgentType: "sync",
	}
	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	var msg SubAgentStartMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Description != "调研 LLM 推理" {
		t.Errorf("description: got %q", msg.Description)
	}
	if msg.Task != event.AgentTask {
		t.Errorf("task: got %q\nwant %q", msg.Task, event.AgentTask)
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

// TestMapAgentIntent guards the new agent.intent wire frame: when the
// main agent (emma) calls a tool, the framework strips the required
// `intent` field and emits an agent_intent EngineEvent with it. The
// mapper must turn that into an agent.intent message carrying the
// progress sentence the user will actually see.
func TestMapAgentIntent(t *testing.T) {
	m := NewEventMapper("s_int", false)
	event := &types.EngineEvent{
		Type:      types.EngineEventAgentIntent,
		AgentID:   "",
		AgentName: "emma",
		ToolUseID: "tu_42",
		ToolName:  "WebSearch",
		Intent:    "正在搜索 vLLM 推理优化的最新论文",
	}
	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(msgs))
	}
	var msg AgentIntentMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != MsgTypeAgentIntent {
		t.Errorf("type: got %q, want %q", msg.Type, MsgTypeAgentIntent)
	}
	if msg.Intent != event.Intent {
		t.Errorf("intent: got %q", msg.Intent)
	}
	if msg.ToolUseID != "tu_42" || msg.ToolName != "WebSearch" {
		t.Errorf("tool attribution lost: %+v", msg)
	}
	if msg.AgentName != "emma" {
		t.Errorf("agent_name: got %q", msg.AgentName)
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

// TestMapSubAgentEnd_ArtifactsReachWire is the regression test for the
// 2026-05-02 wire-drop bug: Engine populated event.Artifacts (from
// SpawnResult.SubmittedArtifacts) but mapSubAgentEnd silently discarded
// it before serialising to the wire. Frontend then saw subagent.end
// without artifacts and couldn't render the produced-files panel.
func TestMapSubAgentEnd_ArtifactsReachWire(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:        types.EngineEventSubAgentEnd,
		AgentID:     "agent_writer",
		AgentName:   "writer",
		AgentStatus: "completed",
		Duration:    8200,
		Terminal:    &types.Terminal{Reason: types.TerminalCompleted, Turn: 3},
		Artifacts: []types.ArtifactRef{
			{
				ArtifactID:  "art_a1b2c3",
				Name:        "intern-schedule-email.md",
				Type:        "file",
				MIMEType:    "text/markdown",
				SizeBytes:   1240,
				Description: "实习生作息安排邮件正稿",
				Role:        "draft_email",
			},
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
	if len(msg.Artifacts) != 1 {
		t.Fatalf("artifacts dropped before wire: want 1, got %d (raw frame: %s)", len(msg.Artifacts), string(msgs[0]))
	}
	got := msg.Artifacts[0]
	if got.ArtifactID != "art_a1b2c3" {
		t.Errorf("wire ArtifactID = %q, want art_a1b2c3", got.ArtifactID)
	}
	if got.Name != "intern-schedule-email.md" {
		t.Errorf("wire Name = %q, want file name surfaced for UI", got.Name)
	}
	if got.Role != "draft_email" {
		t.Errorf("wire Role = %q, want draft_email", got.Role)
	}
}

// TestMapToolEnd_ArtifactsReachWire — same fix on the tool.end path.
// The Specialists / Task tools put aggregated SubmittedArtifacts on
// EngineEvent.Artifacts (via metadata["artifacts"] lifted by executor).
// The mapper used to drop them.
func TestMapToolEnd_ArtifactsReachWire(t *testing.T) {
	m := NewEventMapper("s1", false)
	event := &types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolUseID: "tu_main_1",
		ToolName:  "Specialists",
		ToolResult: &types.ToolResult{
			Content: "完成了 Q4 邮件",
			IsError: false,
			Metadata: map[string]any{
				"render_hint": "agent",
			},
		},
		Artifacts: []types.ArtifactRef{
			{ArtifactID: "art_xxx", Name: "report.md", Type: "file", Role: "draft_email"},
		},
	}

	msgs, err := m.Map(event)
	if err != nil {
		t.Fatal(err)
	}
	var msg ToolEndMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Artifacts) != 1 {
		t.Fatalf("tool.end Artifacts dropped before wire: got %d (frame: %s)", len(msg.Artifacts), string(msgs[0]))
	}
	if msg.Artifacts[0].Name != "report.md" {
		t.Errorf("Artifacts[0].Name = %q, want report.md", msg.Artifacts[0].Name)
	}
}
