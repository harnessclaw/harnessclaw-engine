package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	ws "harnessclaw-go/internal/channel/websocket"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// planLifecycleEngine emits the full plan/step lifecycle so the test
// can verify each event reaches the WebSocket client as a wire frame.
// Mirrors what a real PlanCoordinator + Scheduler emit on a successful
// 2-step run.
type planLifecycleEngine struct{}

func (planLifecycleEngine) ProcessMessage(_ context.Context, _ string, _ *types.Message) (<-chan types.EngineEvent, error) {
	out := make(chan types.EngineEvent, 16)
	go func() {
		defer close(out)
		out <- types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "m1", Model: "test"}
		// plan.created — full DAG up front
		out <- types.EngineEvent{
			Type:    types.EngineEventPlanCreated,
			AgentID: "agent_lf",
			PlanEvent: &types.PlanEvent{
				PlanID:   "plan_agent_lf",
				Goal:     "research+write",
				Strategy: "sequential",
				Status:   "created",
				Tasks: []types.PlanTaskInfo{
					{TaskID: "s1", DependsOn: nil, UserFacingTitle: "research"},
					{TaskID: "s2", DependsOn: []string{"s1"}, UserFacingTitle: "write"},
				},
			},
		}
		// step 1
		out <- types.EngineEvent{Type: types.EngineEventStepDispatched,
			TaskDispatch: &types.TaskDispatch{TaskID: "s1", SubagentType: "researcher", InputSummary: "research"}}
		out <- types.EngineEvent{Type: types.EngineEventStepCompleted,
			TaskDispatch: &types.TaskDispatch{TaskID: "s1", SubagentType: "researcher", OutputSummary: "found 10 sources"}}
		// step 2
		out <- types.EngineEvent{Type: types.EngineEventStepDispatched,
			TaskDispatch: &types.TaskDispatch{TaskID: "s2", SubagentType: "writer", InputSummary: "write"}}
		out <- types.EngineEvent{Type: types.EngineEventStepCompleted,
			TaskDispatch: &types.TaskDispatch{TaskID: "s2", SubagentType: "writer", OutputSummary: "drafted"}}
		// plan completed
		out <- types.EngineEvent{Type: types.EngineEventPlanCompleted,
			AgentID: "agent_lf",
			PlanEvent: &types.PlanEvent{PlanID: "plan_agent_lf", Goal: "research+write", Status: "completed"}}
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}
		out <- types.EngineEvent{Type: types.EngineEventDone, Terminal: &types.Terminal{Reason: types.TerminalCompleted, Turn: 1}}
	}()
	return out, nil
}

func (planLifecycleEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (planLifecycleEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (planLifecycleEngine) SubmitPlanResponse(_ context.Context, _ string, _ *types.PlanResponse) error {
	return nil
}
func (planLifecycleEngine) SubmitStepDecision(_ context.Context, _ string, _ *types.StepDecisionResponse) error {
	return nil
}
func (planLifecycleEngine) AbortSession(_ context.Context, _ string) error { return nil }

// TestE2E_PlanLifecycleReachesClient confirms that plan_created /
// step_dispatched / step_completed / plan_completed events all make it
// through the SpawnSync ParentOut switch (regression guard for v1.16+).
func TestE2E_PlanLifecycleReachesClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	ln.Close()

	logger := zap.NewNop()
	ch := ws.New(
		config.WSChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port, Path: "/ws"},
		func(_ context.Context, _ string) error { return nil },
		logger,
	)
	channels := map[string]channel.Channel{"websocket": ch}
	r := New(planLifecycleEngine{}, channels, nil, nil, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.Start(ctx, r.Handle)
	time.Sleep(150 * time.Millisecond)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	createMsg, _ := json.Marshal(map[string]any{"type": "session.create", "session_id": "lf"})
	_ = conn.Write(ctx, websocket.MessageText, createMsg)
	_, _, _ = conn.Read(ctx)

	userMsg, _ := json.Marshal(map[string]any{"type": "user.message", "text": "trigger"})
	_ = conn.Write(ctx, websocket.MessageText, userMsg)

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	seen := map[string]bool{}
	expected := []string{
		"plan.created", "step.dispatched", "step.completed", "plan.completed",
	}
	for i := 0; i < 20; i++ {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			break
		}
		var env struct{ Type string `json:"type"` }
		_ = json.Unmarshal(data, &env)
		seen[env.Type] = true
		if env.Type == "task.end" {
			break
		}
	}

	for _, t1 := range expected {
		if !seen[t1] {
			t.Errorf("expected %q frame on wire; got none", t1)
		}
	}
}
