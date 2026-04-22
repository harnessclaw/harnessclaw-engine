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

// mockEngine simulates QueryEngine that returns a model error.
type mockEngine struct {
	modelErr error
}

func (m *mockEngine) ProcessMessage(_ context.Context, sessionID string, _ *types.Message) (<-chan types.EngineEvent, error) {
	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)

		// Reproduce the exact sequence from queryloop.go error path:
		// 1. message.start
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: "msg_test",
			Model:     "test-model",
			Usage:     &types.Usage{InputTokens: 10},
		}

		// 2. error event
		out <- types.EngineEvent{Type: types.EngineEventError, Error: m.modelErr}

		// 3. message.delta with stop_reason=error and Error
		out <- types.EngineEvent{
			Type:       types.EngineEventMessageDelta,
			StopReason: "error",
			Error:      m.modelErr,
		}

		// 4. message.stop
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}

		// 5. done with terminal
		out <- types.EngineEvent{
			Type: types.EngineEventDone,
			Terminal: &types.Terminal{
				Reason:  types.TerminalModelError,
				Message: m.modelErr.Error(),
				Turn:    1,
			},
			Usage: &types.Usage{InputTokens: 10},
		}
	}()

	return out, nil
}

func (m *mockEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (m *mockEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (m *mockEngine) AbortSession(_ context.Context, _ string) error {
	return nil
}

// TestE2E_ModelError_RouterToWebSocket tests the full chain:
// WebSocket client → readPump → router → mock engine (error) → SendEvent → writePump → WebSocket client
func TestE2E_ModelError_RouterToWebSocket(t *testing.T) {
	// 1. Start a WebSocket channel on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	ln.Close()

	logger, _ := zap.NewDevelopment()
	ch := ws.New(
		config.WSChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port, Path: "/ws"},
		func(_ context.Context, _ string) error { return nil }, // abortFn
		logger,
	)

	// 2. Create the mock engine and router.
	modelErr := fmt.Errorf("bifrost: stream request failed: provider returned non-SSE response for streaming request")
	eng := &mockEngine{modelErr: modelErr}

	channels := map[string]channel.Channel{"websocket": ch}
	r := New(eng, channels, nil, logger)

	// 3. Start the channel with the router's Handle as the message handler.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ch.Start(ctx, r.Handle)
	time.Sleep(150 * time.Millisecond)

	// 4. Connect a WebSocket client.
	wsConn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal("dial failed:", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// 5. Initialize session.
	createMsg, _ := json.Marshal(map[string]string{
		"type":       "session.create",
		"session_id": "e2e-error-test",
	})
	if err := wsConn.Write(ctx, websocket.MessageText, createMsg); err != nil {
		t.Fatal("write session.create:", err)
	}
	// Read session.created.
	_, _, err = wsConn.Read(ctx)
	if err != nil {
		t.Fatal("read session.created:", err)
	}

	// 6. Send a user message to trigger the error.
	userMsg, _ := json.Marshal(map[string]string{
		"type": "user.message",
		"text": "trigger error",
	})
	if err := wsConn.Write(ctx, websocket.MessageText, userMsg); err != nil {
		t.Fatal("write user.message:", err)
	}

	// 7. Read ALL frames and verify.
	expectedTypes := []string{
		"message.start",
		"error",
		"message.delta",
		"message.stop",
		"task.end",
	}

	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()

	frames := make([][]byte, 0, len(expectedTypes))
	for i := 0; i < len(expectedTypes); i++ {
		_, data, err := wsConn.Read(readCtx)
		if err != nil {
			t.Fatalf("read frame #%d failed: %v\nframes received so far: %d", i, err, len(frames))
			break
		}
		frames = append(frames, data)

		var envelope struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &envelope)
		t.Logf("frame %d: type=%s body=%s", i, envelope.Type, string(data))

		if envelope.Type != expectedTypes[i] {
			t.Errorf("frame %d: expected type %q, got %q", i, expectedTypes[i], envelope.Type)
		}
	}

	if len(frames) < 5 {
		t.Fatalf("only received %d frames, expected 5", len(frames))
	}

	// 8. Verify error frame has message.
	var errFrame struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(frames[1], &errFrame)
	if errFrame.Error.Message == "" {
		t.Error("error frame: missing error.message")
	} else {
		t.Logf("error.message = %q", errFrame.Error.Message)
	}

	// 9. Verify message.delta has error detail.
	var deltaFrame struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"delta"`
	}
	json.Unmarshal(frames[2], &deltaFrame)
	if deltaFrame.Delta.StopReason != "error" {
		t.Errorf("message.delta: stop_reason = %q, want 'error'", deltaFrame.Delta.StopReason)
	}
	if deltaFrame.Delta.Error == nil {
		t.Error("message.delta: delta.error is nil")
	} else if deltaFrame.Delta.Error.Message == "" {
		t.Error("message.delta: delta.error.message is empty")
	}

	// 10. Verify task.end has message.
	var taskEnd struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	json.Unmarshal(frames[4], &taskEnd)
	if taskEnd.Status != "error_model" {
		t.Errorf("task.end: status = %q, want 'error_model'", taskEnd.Status)
	}
	if taskEnd.Message == "" {
		t.Error("task.end: message is empty")
	}

	cancel()
}
