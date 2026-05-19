package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	ws "harnessclaw-go/internal/channel/websocket"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// modeCapturingEngine records the ctx mode value seen on each
// ProcessMessage call. Used by the WS-end-to-end test to confirm the
// wire-level coordinator_mode field actually reaches engine ctx.
type modeCapturingEngine struct {
	mu       sync.Mutex
	gotModes []string
}

func (m *modeCapturingEngine) capture(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotModes = append(m.gotModes, mode)
}

func (m *modeCapturingEngine) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.gotModes...)
}

func (m *modeCapturingEngine) ProcessMessage(ctx context.Context, _ string, _ *types.Message) (<-chan types.EngineEvent, error) {
	m.capture(tool.GetCoordinatorMode(ctx))
	out := make(chan types.EngineEvent, 4)
	go func() {
		defer close(out)
		out <- types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_x", Model: "test"}
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}
		out <- types.EngineEvent{Type: types.EngineEventDone,
			Terminal: &types.Terminal{Reason: types.TerminalCompleted, Turn: 1},
		}
	}()
	return out, nil
}

func (m *modeCapturingEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (m *modeCapturingEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (m *modeCapturingEngine) SubmitPlanResponse(_ context.Context, _ string, _ *types.PlanResponse) error {
	return nil
}
func (m *modeCapturingEngine) SubmitStepDecision(_ context.Context, _ string, _ *types.StepDecisionResponse) error {
	return nil
}
func (m *modeCapturingEngine) AbortSession(_ context.Context, _ string) error { return nil }

// TestE2E_WebSocketCoordinatorModeWiring proves the full wire path:
// JSON{coordinator_mode:"plan"} → WS protocol → router → engine ctx.
//
// Without this, regressions in the WS schema or the IncomingMessage→ctx
// hop go silently undetected — operators would think they enabled plan
// mode while the engine still ran ReAct.
func TestE2E_WebSocketCoordinatorModeWiring(t *testing.T) {
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

	eng := &modeCapturingEngine{}
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

	// session.create
	createMsg, _ := json.Marshal(map[string]any{
		"type":       "session.create",
		"session_id": "e2e-mode-test",
	})
	if err := conn.Write(ctx, websocket.MessageText, createMsg); err != nil {
		t.Fatal("write session.create:", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal("read session.created:", err)
	}

	// First user.message with coordinator_mode = plan.
	planMsg, _ := json.Marshal(map[string]any{
		"type":             "user.message",
		"text":             "调研 X 写 Y",
		"coordinator_mode": "plan",
	})
	if err := conn.Write(ctx, websocket.MessageText, planMsg); err != nil {
		t.Fatal("write user.message[plan]:", err)
	}
	// Read the events to drain — we don't care about content here.
	drainCtx, drainCancel := context.WithTimeout(ctx, 2*time.Second)
	for i := 0; i < 3; i++ {
		if _, _, err := conn.Read(drainCtx); err != nil {
			break
		}
	}
	drainCancel()

	// Second message without mode — should appear empty in ctx.
	defaultMsg, _ := json.Marshal(map[string]any{
		"type": "user.message",
		"text": "翻译这句话",
	})
	if err := conn.Write(ctx, websocket.MessageText, defaultMsg); err != nil {
		t.Fatal("write user.message[default]:", err)
	}
	drainCtx2, drainCancel2 := context.WithTimeout(ctx, 2*time.Second)
	for i := 0; i < 3; i++ {
		if _, _, err := conn.Read(drainCtx2); err != nil {
			break
		}
	}
	drainCancel2()

	// Allow the router's background goroutine to finish forwarding.
	time.Sleep(100 * time.Millisecond)

	got := eng.snapshot()
	if len(got) < 2 {
		t.Fatalf("expected at least 2 ProcessMessage calls; got %d (%v)", len(got), got)
	}
	if got[0] != "plan" {
		t.Errorf("first message: ctx mode = %q, want plan", got[0])
	}
	if got[1] != "" {
		t.Errorf("second message (no mode): ctx mode = %q, want empty", got[1])
	}
}
