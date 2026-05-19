package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	ws "harnessclaw-go/internal/channel/websocket"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// planProposingEngine is a mock that simulates a coordinator emitting
// the full plan lifecycle (plan.created → plan.proposed → step.* → plan.completed).
// The test confirms the full pipeline (engine event → router →
// ch.SendEvent → mapper → wire JSON) reaches the WebSocket client.
// Guards against regressions where new event types are silently dropped
// by the SpawnSync ParentOut switch.
type planProposingEngine struct {
	mu       sync.Mutex
	gotPlanResponse *types.PlanResponse
}

func (m *planProposingEngine) ProcessMessage(_ context.Context, _ string, _ *types.Message) (<-chan types.EngineEvent, error) {
	out := make(chan types.EngineEvent, 8)
	go func() {
		defer close(out)
		out <- types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_x", Model: "test"}
		out <- types.EngineEvent{
			Type:    types.EngineEventPlanProposed,
			AgentID: "agent_test",
			PlanProposal: &types.PlanProposal{
				PlanID:    "pln_test_e2e_001",
				AgentID:   "agent_test",
				Goal:      "test goal",
				Rationale: "test rationale",
				Steps: []types.ProposedStep{
					{ID: "s1", SubagentType: "researcher", Prompt: "research"},
					{ID: "s2", SubagentType: "writer", Prompt: "write", DependsOn: []string{"s1"}},
				},
				AvailableSubagents: []string{"researcher", "writer", "analyst"},
			},
		}
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}
		out <- types.EngineEvent{Type: types.EngineEventDone,
			Terminal: &types.Terminal{Reason: types.TerminalCompleted, Turn: 1},
		}
	}()
	return out, nil
}

func (m *planProposingEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (m *planProposingEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (m *planProposingEngine) SubmitPlanResponse(_ context.Context, _ string, resp *types.PlanResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotPlanResponse = resp
	return nil
}
func (m *planProposingEngine) SubmitStepDecision(_ context.Context, _ string, _ *types.StepDecisionResponse) error {
	return nil
}
func (m *planProposingEngine) AbortSession(_ context.Context, _ string) error { return nil }

// TestE2E_PlanProposedReachesClient is the integration test that proves
// PlanProposed events make it from engine emission all the way to a WS
// client as a `plan.proposed` JSON frame. Setup is the same shape as
// TestE2E_ModelError_RouterToWebSocket.
func TestE2E_PlanProposedReachesClient(t *testing.T) {
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

	eng := &planProposingEngine{}
	channels := map[string]channel.Channel{"websocket": ch}
	r := New(eng, channels, nil, nil, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ch.Start(ctx, r.Handle)
	time.Sleep(150 * time.Millisecond)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	createMsg, _ := json.Marshal(map[string]any{
		"type":       "session.create",
		"session_id": "e2e-plan-proposal",
	})
	if err := conn.Write(ctx, websocket.MessageText, createMsg); err != nil {
		t.Fatal("write session.create:", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal("read session.created:", err)
	}

	// Send a user.message; the mock engine will emit plan.proposed
	// among other events.
	userMsg, _ := json.Marshal(map[string]any{
		"type":             "user.message",
		"text":             "trigger plan",
		"coordinator_mode": "plan",
		"plan_confirmation": "required",
	})
	if err := conn.Write(ctx, websocket.MessageText, userMsg); err != nil {
		t.Fatal("write user.message:", err)
	}

	// Read frames until we either find plan.proposed or hit done.
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	var planProposedSeen bool
	var planProposedRaw []byte
	for i := 0; i < 10; i++ {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			break
		}
		var env struct{ Type string `json:"type"` }
		_ = json.Unmarshal(data, &env)
		t.Logf("frame %d type=%s", i, env.Type)
		if env.Type == "plan.proposed" {
			planProposedSeen = true
			planProposedRaw = data
			break
		}
		if env.Type == "task.end" {
			break
		}
	}

	if !planProposedSeen {
		t.Fatalf("plan.proposed never reached client — regression of SpawnSync filter or mapper wiring")
	}

	// Verify the wire body carries the structured fields.
	var got struct {
		Type   string `json:"type"`
		PlanID string `json:"plan_id"`
		Goal   string `json:"goal"`
		Steps  []struct {
			ID           string `json:"id"`
			SubagentType string `json:"subagent_type"`
		} `json:"steps"`
		AvailableSubagents []string `json:"available_subagents"`
	}
	if err := json.Unmarshal(planProposedRaw, &got); err != nil {
		t.Fatalf("plan.proposed not valid JSON: %v\nframe: %s", err, planProposedRaw)
	}
	if got.PlanID != "pln_test_e2e_001" {
		t.Errorf("plan_id mismatch; got %q", got.PlanID)
	}
	if got.Goal != "test goal" {
		t.Errorf("goal mismatch; got %q", got.Goal)
	}
	if len(got.Steps) != 2 {
		t.Errorf("expected 2 steps; got %d", len(got.Steps))
	}
	if !strings.Contains(strings.Join(got.AvailableSubagents, ","), "researcher") {
		t.Errorf("available_subagents missing researcher; got %v", got.AvailableSubagents)
	}

	// Quick check on the inverse path — send a plan.response and confirm
	// the engine receives it (proves connection.go decoding works).
	respMsg, _ := json.Marshal(map[string]any{
		"type":          "plan.response",
		"plan_id":       "pln_test_e2e_001",
		"plan_approved": true,
	})
	if err := conn.Write(ctx, websocket.MessageText, respMsg); err != nil {
		t.Fatal("write plan.response:", err)
	}
	time.Sleep(150 * time.Millisecond)

	eng.mu.Lock()
	gotResp := eng.gotPlanResponse
	eng.mu.Unlock()
	if gotResp == nil {
		t.Fatal("plan.response did not reach engine")
	}
	if gotResp.PlanID != "pln_test_e2e_001" || !gotResp.Approved {
		t.Errorf("plan.response decoded wrong: %+v", gotResp)
	}
}
