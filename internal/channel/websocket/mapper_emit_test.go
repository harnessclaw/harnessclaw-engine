package websocket

import (
	"encoding/json"
	"testing"
	"time"

	"harnessclaw-go/internal/emit"
	"harnessclaw-go/pkg/types"
)

func newTestEnvelope(traceID string, seq int64) *emit.Envelope {
	return &emit.Envelope{
		EventID:    "evt_test",
		TraceID:    traceID,
		Seq:        seq,
		Timestamp:  time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC),
		AgentRole:  emit.RolePersona,
		AgentID:    "emma",
		AgentRunID: "run_xx",
		Severity:   emit.SeverityInfo,
	}
}

func TestMapTraceStarted(t *testing.T) {
	m := NewEventMapper("sess_1", false)
	out, err := m.Map(&types.EngineEvent{
		Type:     types.EngineEventTraceStarted,
		Text:     "user wants to query sales data",
		Envelope: newTestEnvelope("tr_1", 1),
		Display: &emit.Display{
			Title:      "新对话开始",
			Visibility: emit.VisibilityCollapsed,
		},
	})
	if err != nil {
		t.Fatalf("map error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	var got TraceStartedMessage
	if err := json.Unmarshal(out[0], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != MsgTypeTraceStarted {
		t.Errorf("type: got %q want %q", got.Type, MsgTypeTraceStarted)
	}
	if got.Envelope == nil || got.Envelope.TraceID != "tr_1" || got.Envelope.Seq != 1 {
		t.Errorf("envelope mismatch: %+v", got.Envelope)
	}
	if got.Envelope.AgentRole != "persona" {
		t.Errorf("agent_role: got %q", got.Envelope.AgentRole)
	}
	if got.Display == nil || got.Display.Title != "新对话开始" {
		t.Errorf("display: %+v", got.Display)
	}
	if got.Payload.UserInputSummary != "user wants to query sales data" {
		t.Errorf("payload.user_input_summary: %q", got.Payload.UserInputSummary)
	}
	// Top-level event_id and session_id MUST be present (envelope is additive).
	if got.EventID == "" {
		t.Errorf("top-level event_id missing")
	}
	if got.SessionID != "sess_1" {
		t.Errorf("top-level session_id missing/wrong: %q", got.SessionID)
	}
}

func TestMapPlanCreated(t *testing.T) {
	m := NewEventMapper("sess_1", false)
	out, err := m.Map(&types.EngineEvent{
		Type:     types.EngineEventPlanCreated,
		Envelope: newTestEnvelope("tr_2", 5),
		Display:  &emit.Display{Title: "计划已制定", Icon: emit.IconPlan},
		PlanEvent: &types.PlanEvent{
			PlanID:   "plan_a",
			Goal:     "查询并对比销量",
			Strategy: "parallel",
			Status:   "created",
			Tasks: []types.PlanTaskInfo{
				{
					TaskID:          "t_001",
					SubagentType:    "search_agent",
					UserFacingTitle: "查 2024 销量",
				},
				{
					TaskID:          "t_002",
					SubagentType:    "analysis_agent",
					DependsOn:       []string{"t_001"},
					UserFacingTitle: "对比同比",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("map error: %v", err)
	}
	var got PlanCreatedMessage
	if err := json.Unmarshal(out[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Payload.PlanID != "plan_a" || got.Payload.Strategy != "parallel" {
		t.Errorf("plan payload: %+v", got.Payload)
	}
	if len(got.Payload.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(got.Payload.Tasks))
	}
	if got.Payload.Tasks[1].DependsOn[0] != "t_001" {
		t.Errorf("depends_on lost: %+v", got.Payload.Tasks[1])
	}
	if got.Display.Icon != "plan" {
		t.Errorf("icon: got %q", got.Display.Icon)
	}
}

func TestMapStepDispatchedAndCompleted(t *testing.T) {
	m := NewEventMapper("sess_1", false)

	// dispatched
	dispatched, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventStepDispatched,
		Envelope: newTestEnvelope("tr_3", 7),
		TaskDispatch: &types.TaskDispatch{
			TaskID:       "s1",
			SubagentType: "search_agent",
			InputSummary: "find Q4 numbers",
		},
	})
	var dMsg StepDispatchedMessage
	if err := json.Unmarshal(dispatched[0], &dMsg); err != nil {
		t.Fatal(err)
	}
	if dMsg.Type != MsgTypeStepDispatched {
		t.Errorf("type wrong: %q", dMsg.Type)
	}
	if dMsg.Payload.StepID != "s1" || dMsg.Payload.SubagentType != "search_agent" {
		t.Errorf("dispatched payload: %+v", dMsg.Payload)
	}

	// completed
	completed, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventStepCompleted,
		Envelope: newTestEnvelope("tr_3", 8),
		Metrics:  &emit.Metrics{DurationMs: 1500, TokensIn: 80, TokensOut: 320},
		TaskDispatch: &types.TaskDispatch{
			TaskID:        "s1",
			OutputSummary: "found 10 reports",
			Attempts:      1,
			Deliverables:  []string{"/tmp/out.md"},
		},
	})
	var cMsg StepCompletedMessage
	if err := json.Unmarshal(completed[0], &cMsg); err != nil {
		t.Fatal(err)
	}
	if cMsg.Type != MsgTypeStepCompleted {
		t.Errorf("type wrong: %q", cMsg.Type)
	}
	if cMsg.Metrics == nil || cMsg.Metrics.DurationMs != 1500 {
		t.Errorf("metrics lost: %+v", cMsg.Metrics)
	}
	if cMsg.Payload.StepID != "s1" || cMsg.Payload.OutputSummary != "found 10 reports" || len(cMsg.Payload.Deliverables) != 1 {
		t.Errorf("completed payload: %+v", cMsg.Payload)
	}
}

func TestMapStepFailedSplitsErrorAndUserMessage(t *testing.T) {
	m := NewEventMapper("sess_1", false)
	out, err := m.Map(&types.EngineEvent{
		Type:     types.EngineEventStepFailed,
		Envelope: newTestEnvelope("tr_4", 12),
		TaskDispatch: &types.TaskDispatch{
			TaskID:      "s2",
			ErrorType:   string(emit.ErrorTypeToolTimeout),
			ErrorCode:   "WEBFETCH_TIMEOUT",
			Error:       "webfetch deadline exceeded after 120s",
			UserMessage: "查得有点慢，我换个方法",
			Retryable:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg StepFailedMessage
	if err := json.Unmarshal(out[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Payload.Error.Type != string(emit.ErrorTypeToolTimeout) {
		t.Errorf("error.type lost: %+v", msg.Payload.Error)
	}
	if msg.Payload.Error.Code != "WEBFETCH_TIMEOUT" {
		t.Errorf("error.code lost: %+v", msg.Payload.Error)
	}
	if msg.Payload.Error.Message == "" || msg.Payload.Error.UserMessage == "" {
		t.Errorf("error split lost: %+v", msg.Payload.Error)
	}
	if !msg.Payload.Error.Retryable {
		t.Errorf("retryable flag lost")
	}
}

func TestMapPlanFailedHasError(t *testing.T) {
	m := NewEventMapper("sess_1", false)
	out, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventPlanFailed,
		Envelope: newTestEnvelope("tr_pf", 30),
		TaskDispatch: &types.TaskDispatch{
			TaskID:    "plan_x",
			ErrorType: string(emit.ErrorTypeDependencyFail),
			Error:     "all 3 steps failed",
		},
		Display: &emit.Display{Title: "计划失败", Icon: emit.IconError},
	})
	var msg PlanFailedMessage
	if err := json.Unmarshal(out[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != MsgTypePlanFailed {
		t.Errorf("type wrong: %q", msg.Type)
	}
	if msg.Payload.Error.Type != string(emit.ErrorTypeDependencyFail) {
		t.Errorf("error.type lost: %+v", msg.Payload.Error)
	}
}

func TestMapTraceFailedFallsBackToInternalErrorType(t *testing.T) {
	// Producer didn't set ErrorType — mapper must default to internal_error
	// so the wire payload always carries a type field.
	m := NewEventMapper("sess_1", false)
	out, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventTraceFailed,
		Envelope: newTestEnvelope("tr_tf", 40),
		Error:    errStub("kaboom"),
	})
	var msg TraceFailedMessage
	if err := json.Unmarshal(out[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Payload.Error.Type != string(emit.ErrorTypeInternal) {
		t.Errorf("expected internal_error fallback, got %q", msg.Payload.Error.Type)
	}
	if msg.Payload.Error.Message != "kaboom" {
		t.Errorf("error.message lost: %+v", msg.Payload.Error)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

func TestMapTraceFinishedSetsNumTurns(t *testing.T) {
	m := NewEventMapper("sess_1", false)
	out, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventTraceFinished,
		Envelope: newTestEnvelope("tr_5", 22),
		Metrics:  &emit.Metrics{DurationMs: 2950, TokensIn: 350, TokensOut: 120, Model: "claude-opus-4-7"},
		Terminal: &types.Terminal{Turn: 3},
	})
	var msg TraceFinishedMessage
	if err := json.Unmarshal(out[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Payload.NumTurns != 3 {
		t.Errorf("num_turns: got %d want 3", msg.Payload.NumTurns)
	}
	if msg.Metrics.Model != "claude-opus-4-7" {
		t.Errorf("metrics model lost: %+v", msg.Metrics)
	}
}
