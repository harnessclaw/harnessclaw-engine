package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// ============================================================
// Mapper unit tests (v1.2 protocol)
// ============================================================

func TestMapTextEvent_FirstEmitsBlockStart(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (content.start + content.delta), got %d", len(msgs))
	}

	var start ContentStartMessage
	if err := json.Unmarshal(msgs[0], &start); err != nil {
		t.Fatal(err)
	}
	if start.Type != MsgTypeContentStart {
		t.Errorf("expected %s, got %s", MsgTypeContentStart, start.Type)
	}
	if start.ContentBlock == nil || start.ContentBlock.Type != "text" {
		t.Error("expected content_block with type 'text'")
	}

	var delta ContentDeltaMessage
	if err := json.Unmarshal(msgs[1], &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Type != MsgTypeContentDelta {
		t.Errorf("expected %s, got %s", MsgTypeContentDelta, delta.Type)
	}
	if delta.Delta == nil || delta.Delta.Text != "hello" {
		t.Error("expected delta text 'hello'")
	}
}

func TestMapTextEvent_SubsequentOnlyDelta(t *testing.T) {
	m := NewEventMapper("s1", false)
	// First call opens the block.
	_, _ = m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "a"})

	// Second call should only emit one delta.
	msgs, err := m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (delta only), got %d", len(msgs))
	}
}

func TestMapToolStartEvent(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolUseID: "tu_1",
		ToolName:  "bash",
		ToolInput: `{"command":"ls -la"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (tool.start), got %d", len(msgs))
	}

	var msg ToolStartMessage
	if err := json.Unmarshal(msgs[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != MsgTypeToolStart {
		t.Errorf("expected %s, got %s", MsgTypeToolStart, msg.Type)
	}
	if msg.ToolName != "bash" {
		t.Errorf("expected tool name 'bash', got %q", msg.ToolName)
	}
	if msg.ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id 'tu_1', got %q", msg.ToolUseID)
	}
	if cmd, ok := msg.Input["command"]; !ok || cmd != "ls -la" {
		t.Errorf("expected input.command 'ls -la', got %v", msg.Input)
	}
}

func TestMapToolStartEvent_ClosesOpenTextBlock(t *testing.T) {
	m := NewEventMapper("s1", false)
	// Open a text block.
	_, _ = m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "hi"})
	// tool_use (from LLM) should close the text block.
	// In server-side mode (clientTools=false), tool_use content blocks are suppressed.
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolUse,
		ToolUseID: "tu_1",
		ToolName:  "grep",
		ToolInput: `{"pattern":"foo"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Expect: content.stop (for text) only — tool_use content is suppressed in server-side mode.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (content.stop for text), got %d", len(msgs))
	}

	var stop ContentStopMessage
	json.Unmarshal(msgs[0], &stop)
	if stop.Type != MsgTypeContentStop {
		t.Errorf("expected %s, got %s", MsgTypeContentStop, stop.Type)
	}
}

func TestMapToolUse_ClientMode_EmitsContentBlocks(t *testing.T) {
	m := NewEventMapper("s1", true) // client-tools mode
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolUse,
		ToolUseID: "tu_1",
		ToolName:  "bash",
		ToolInput: `{"command":"ls"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Expect: content.start + content.delta + content.stop
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (content.start + content.delta + content.stop), got %d", len(msgs))
	}

	var start ContentStartMessage
	json.Unmarshal(msgs[0], &start)
	if start.Type != MsgTypeContentStart {
		t.Errorf("expected content.start, got %s", start.Type)
	}
	if start.ContentBlock == nil || start.ContentBlock.Type != "tool_use" {
		t.Error("expected tool_use content block")
	}
	if start.ContentBlock.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", start.ContentBlock.Name)
	}

	var delta ContentDeltaMessage
	json.Unmarshal(msgs[1], &delta)
	if delta.Type != MsgTypeContentDelta {
		t.Errorf("expected content.delta, got %s", delta.Type)
	}
	if delta.Delta == nil || delta.Delta.Type != "input_json_delta" {
		t.Error("expected input_json_delta")
	}

	var stop ContentStopMessage
	json.Unmarshal(msgs[2], &stop)
	if stop.Type != MsgTypeContentStop {
		t.Errorf("expected content.stop, got %s", stop.Type)
	}
}

func TestMapToolUse_ServerMode_SuppressesContentBlocks(t *testing.T) {
	m := NewEventMapper("s1", false) // server-side mode
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolUse,
		ToolUseID: "tu_1",
		ToolName:  "bash",
		ToolInput: `{"command":"ls"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// In server-side mode, tool_use content blocks are suppressed (tool.start carries the info).
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages in server-side mode, got %d", len(msgs))
	}
}

func TestMapToolEndEvent(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolUseID: "tu_1",
		ToolName:  "bash",
		ToolResult: &types.ToolResult{
			Content: "file1.go\nfile2.go",
			IsError: false,
			Metadata: map[string]any{
				"exit_code":   0,
				"duration_ms": int64(42),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (tool.end), got %d", len(msgs))
	}
	var msg ToolEndMessage
	json.Unmarshal(msgs[0], &msg)
	if msg.Type != MsgTypeToolEnd {
		t.Errorf("expected %s, got %s", MsgTypeToolEnd, msg.Type)
	}
	if msg.ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id 'tu_1', got %q", msg.ToolUseID)
	}
	if msg.Status != "success" {
		t.Errorf("expected status 'success', got %q", msg.Status)
	}
	if msg.Output != "file1.go\nfile2.go" {
		t.Errorf("expected output, got %q", msg.Output)
	}
	if msg.DurationMs != 42 {
		t.Errorf("expected duration_ms 42, got %d", msg.DurationMs)
	}
	// duration_ms should be promoted to top-level, not duplicated in metadata.
	if msg.Metadata != nil {
		if _, hasDur := msg.Metadata["duration_ms"]; hasDur {
			t.Error("duration_ms should not be duplicated in metadata")
		}
	}
	// exit_code should remain in metadata.
	if msg.Metadata == nil {
		t.Fatal("expected metadata with exit_code")
	}
	ec, ok := msg.Metadata["exit_code"]
	if !ok {
		t.Fatal("metadata missing exit_code")
	}
	// After JSON roundtrip, integer values in map[string]any become float64.
	if ecf, isFloat := ec.(float64); !isFloat || ecf != 0 {
		t.Errorf("expected metadata.exit_code=0 (float64), got %v (%T)", ec, ec)
	}
}

func TestMapErrorEvent(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:  types.EngineEventError,
		Error: fmt.Errorf("something broke"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var errMsg ErrorMessage
	json.Unmarshal(msgs[0], &errMsg)
	if errMsg.Type != MsgTypeError {
		t.Errorf("expected 'error', got %q", errMsg.Type)
	}
	if errMsg.Error.Type != "internal_error" {
		t.Errorf("expected error.type 'internal_error', got %q", errMsg.Error.Type)
	}
	if errMsg.Error.Message != "something broke" {
		t.Errorf("expected 'something broke', got %q", errMsg.Error.Message)
	}
}

func TestMapDoneEvent_Success(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type: types.EngineEventDone,
		Terminal: &types.Terminal{
			Reason: types.TerminalCompleted,
			Turn:   3,
		},
		Usage: &types.Usage{InputTokens: 100, OutputTokens: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var te TaskEndMessage
	json.Unmarshal(msgs[0], &te)
	if te.Type != MsgTypeTaskEnd {
		t.Errorf("expected %s, got %s", MsgTypeTaskEnd, te.Type)
	}
	if te.Status != "success" {
		t.Errorf("expected 'success', got %q", te.Status)
	}
	if te.NumTurns != 3 {
		t.Errorf("expected 3 turns, got %d", te.NumTurns)
	}
	if te.TotalUsage == nil || te.TotalUsage.InputTokens != 100 {
		t.Error("expected usage with 100 input tokens")
	}
}

func TestMapDoneEvent_MaxTurns(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventDone,
		Terminal: &types.Terminal{Reason: types.TerminalMaxTurns, Turn: 50},
	})
	var te TaskEndMessage
	json.Unmarshal(msgs[len(msgs)-1], &te)
	if te.Status != "error_max_turns" {
		t.Errorf("expected 'error_max_turns', got %q", te.Status)
	}
}

func TestMapDoneEvent_Aborted(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventDone,
		Terminal: &types.Terminal{Reason: types.TerminalAbortedStreaming},
	})
	var te TaskEndMessage
	json.Unmarshal(msgs[len(msgs)-1], &te)
	if te.Status != "aborted" {
		t.Errorf("expected 'aborted', got %q", te.Status)
	}
}

func TestMapDoneEvent_ClosesOpenTextBlock(t *testing.T) {
	m := NewEventMapper("s1", false)
	_, _ = m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "x"})
	msgs, _ := m.Map(&types.EngineEvent{
		Type:     types.EngineEventDone,
		Terminal: &types.Terminal{Reason: types.TerminalCompleted},
	})
	// Expect: content.stop + task.end
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (stop + task.end), got %d", len(msgs))
	}
}

func TestMapperReset(t *testing.T) {
	m := NewEventMapper("s1", false)
	_, _ = m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "a"})
	m.Reset()

	if m.blockIndex != 0 {
		t.Errorf("expected blockIndex 0 after reset, got %d", m.blockIndex)
	}
	if m.inTextBlock {
		t.Error("expected inTextBlock false after reset")
	}

	// After reset, first text should emit content.start again.
	msgs, _ := m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "b"})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after reset, got %d", len(msgs))
	}
}

// ============================================================
// Registry unit tests
// ============================================================

func TestRegistry_RegisterUnregister(t *testing.T) {
	r := NewConnRegistry()
	c := &Conn{id: "c1", sessionID: "s1"}
	r.Register(c)
	if got := r.GetBySession("s1"); len(got) != 1 {
		t.Fatalf("expected 1 conn, got %d", len(got))
	}

	r.Unregister("s1", "c1")
	if got := r.GetBySession("s1"); len(got) != 0 {
		t.Fatalf("expected 0 conns after unregister, got %d", len(got))
	}
}

func TestRegistry_MultipleConnsPerSession(t *testing.T) {
	r := NewConnRegistry()
	r.Register(&Conn{id: "c1", sessionID: "s1"})
	r.Register(&Conn{id: "c2", sessionID: "s1"})
	r.Register(&Conn{id: "c3", sessionID: "s2"})

	if got := r.GetBySession("s1"); len(got) != 2 {
		t.Errorf("expected 2 conns for s1, got %d", len(got))
	}
	if got := r.All(); len(got) != 3 {
		t.Errorf("expected 3 total conns, got %d", len(got))
	}
}

// ============================================================
// WebSocket integration test
// ============================================================

// startTestChannel spins up a Channel on a random port and returns it + addr.
func startTestChannel(t *testing.T) (*Channel, string) {
	t.Helper()

	// Find a free port.
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
	ch := New(
		config.WSChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port, Path: "/ws"},
		nil, // no abortFn for tests
		logger,
	)
	return ch, addr
}

func TestIntegration_WebSocket_RoundTrip(t *testing.T) {
	ch, addr := startTestChannel(t)

	// Mock handler: when a "user" message arrives, inject a series of
	// EngineEvents through SendEvent.
	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		events := []types.EngineEvent{
			{Type: types.EngineEventText, Text: "Hello"},
			{Type: types.EngineEventText, Text: " World"},
			{Type: types.EngineEventDone, Terminal: &types.Terminal{
				Reason: types.TerminalCompleted, Turn: 1,
			}, Usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		}
		for i := range events {
			if err := ch.SendEvent(ctx, msg.SessionID, &events[i]); err != nil {
				return err
			}
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start channel in background.
	startErr := make(chan error, 1)
	go func() { startErr <- ch.Start(ctx, mockHandler) }()

	// Wait for server to be ready.
	time.Sleep(100 * time.Millisecond)

	if err := ch.Health(); err != nil {
		t.Fatal("channel not healthy:", err)
	}

	// Connect a WebSocket client.
	wsURL := "ws://" + addr + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal("dial failed:", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "test done")

	// Send session.create to initialize the connection.
	createMsg, _ := json.Marshal(ClientMessage{
		Type:      MsgTypeSessionCreate,
		SessionID: "test-session",
	})
	if err := ws.Write(ctx, websocket.MessageText, createMsg); err != nil {
		t.Fatal("write session.create:", err)
	}

	// Read session.created response.
	_, initData, err := ws.Read(ctx)
	if err != nil {
		t.Fatal("read init:", err)
	}
	var initMsg SessionCreatedMessage
	json.Unmarshal(initData, &initMsg)
	if initMsg.Type != MsgTypeSessionCreated {
		t.Errorf("expected session.created, got %s", initMsg.Type)
	}
	if initMsg.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", initMsg.SessionID)
	}
	if initMsg.ProtocolVersion != "1.5" {
		t.Errorf("expected protocol_version '1.5', got %q", initMsg.ProtocolVersion)
	}

	// Send a user.message.
	userMsg := ClientMessage{Type: MsgTypeUserMessage, EventID: "evt_u1", Text: "hello"}
	userData, _ := json.Marshal(userMsg)
	if err := ws.Write(ctx, websocket.MessageText, userData); err != nil {
		t.Fatal("write failed:", err)
	}

	// Collect server responses. We expect:
	// 1. content.start (text)
	// 2. content.delta "Hello"
	// 3. content.delta " World"
	// 4. content.stop (from done closing text block)
	// 5. task.end
	var received []json.RawMessage
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()

	for i := 0; i < 5; i++ {
		_, data, err := ws.Read(readCtx)
		if err != nil {
			t.Fatalf("read #%d failed: %v", i, err)
		}
		received = append(received, data)
	}

	// Verify message types in order.
	expectedTypes := []string{
		"content.start", // block_start
		"content.delta", // delta "Hello"
		"content.delta", // delta " World"
		"content.stop",  // block_stop (from done closing text block)
		"task.end",      // task end
	}
	for i, raw := range received {
		var envelope struct {
			Type string `json:"type"`
		}
		json.Unmarshal(raw, &envelope)
		if envelope.Type != expectedTypes[i] {
			t.Errorf("message %d: expected type %q, got %q", i, expectedTypes[i], envelope.Type)
		}
	}

	// Verify the task.end message.
	var taskEnd TaskEndMessage
	json.Unmarshal(received[4], &taskEnd)
	if taskEnd.Status != "success" {
		t.Errorf("expected status 'success', got %q", taskEnd.Status)
	}
	if taskEnd.NumTurns != 1 {
		t.Errorf("expected 1 turn, got %d", taskEnd.NumTurns)
	}
	if taskEnd.TotalUsage == nil || taskEnd.TotalUsage.InputTokens != 10 {
		t.Error("expected usage with 10 input tokens")
	}

	// Shutdown.
	cancel()
	select {
	case err := <-startErr:
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("unexpected start error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("shutdown timed out")
	}
}

func TestIntegration_WebSocket_MultipleClients(t *testing.T) {
	ch, addr := startTestChannel(t)

	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		return ch.SendEvent(ctx, msg.SessionID, &types.EngineEvent{
			Type: types.EngineEventText, Text: "reply",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.Start(ctx, mockHandler)
	time.Sleep(100 * time.Millisecond)

	// Connect two clients to the same session.
	wsURL := "ws://" + addr + "/ws"
	ws1, _, _ := websocket.Dial(ctx, wsURL, nil)
	defer ws1.Close(websocket.StatusNormalClosure, "done")
	ws2, _, _ := websocket.Dial(ctx, wsURL, nil)
	defer ws2.Close(websocket.StatusNormalClosure, "done")

	// Send session.create for both clients (same session).
	createMsg, _ := json.Marshal(ClientMessage{
		Type:      MsgTypeSessionCreate,
		SessionID: "shared",
	})
	ws1.Write(ctx, websocket.MessageText, createMsg)
	ws2.Write(ctx, websocket.MessageText, createMsg)

	// Drain session.created responses.
	ws1.Read(ctx)
	ws2.Read(ctx)

	// Send from client 1.
	msg, _ := json.Marshal(ClientMessage{Type: MsgTypeUserMessage, EventID: "evt_u1", Text: "hi"})
	ws1.Write(ctx, websocket.MessageText, msg)

	// Both clients should receive the text event (fan-out).
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	for i, ws := range []*websocket.Conn{ws1, ws2} {
		// Each client gets: content.start + content.delta
		for j := 0; j < 2; j++ {
			_, _, err := ws.Read(readCtx)
			if err != nil {
				t.Fatalf("client %d, read %d failed: %v", i, j, err)
			}
		}
	}
}

func TestTrySend_NonBlocking(t *testing.T) {
	c := &Conn{
		id:        "c1",
		sessionID: "s1",
		send:      make(chan []byte, 2),
		done:      make(chan struct{}),
		logger:    zap.NewNop(),
	}

	// Fill the buffer.
	c.TrySend([]byte("1"))
	c.TrySend([]byte("2"))

	// Third send should not block and return false.
	if c.TrySend([]byte("3")) {
		t.Error("expected TrySend to return false on full buffer")
	}
}

// ============================================================
// Terminal reason mapping
// ============================================================

func TestMapTerminalReason(t *testing.T) {
	cases := []struct {
		reason types.TerminalReason
		want   string
	}{
		{types.TerminalCompleted, "success"},
		{types.TerminalMaxTurns, "error_max_turns"},
		{types.TerminalModelError, "error_model"},
		{types.TerminalAbortedStreaming, "aborted"},
		{types.TerminalAbortedTools, "aborted"},
		{types.TerminalPromptTooLong, "error"},
		{types.TerminalBlockingLimit, "error"},
	}
	for _, tc := range cases {
		got := mapTerminalReason(tc.reason)
		if got != tc.want {
			t.Errorf("mapTerminalReason(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

// ============================================================
// v1.1 specific tests
// ============================================================

func TestMapEventID_HasPrefix(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, _ := m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "hi"})
	var msg ContentStartMessage
	json.Unmarshal(msgs[0], &msg)
	if len(msg.EventID) < 4 || msg.EventID[:4] != "evt_" {
		t.Errorf("expected event_id with 'evt_' prefix, got %q", msg.EventID)
	}
}

func TestMapError_StructuredError(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, _ := m.Map(&types.EngineEvent{
		Type:  types.EngineEventError,
		Error: fmt.Errorf("rate limit hit"),
	})
	var errMsg ErrorMessage
	json.Unmarshal(msgs[0], &errMsg)
	if errMsg.Type != MsgTypeError {
		t.Errorf("expected 'error', got %q", errMsg.Type)
	}
	if errMsg.Error.Code != "engine_error" {
		t.Errorf("expected error.code 'engine_error', got %q", errMsg.Error.Code)
	}
}

// ============================================================
// v1.1 message lifecycle tests
// ============================================================

func TestMapMessageStart(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_abc",
		Model:     "claude-3",
		Usage:     &types.Usage{InputTokens: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg MessageStartMessage
	json.Unmarshal(msgs[0], &msg)
	if msg.Type != MsgTypeMessageStart {
		t.Errorf("expected message.start, got %s", msg.Type)
	}
	if msg.Message.ID != "msg_abc" {
		t.Errorf("expected message id 'msg_abc', got %q", msg.Message.ID)
	}
	if msg.Message.Model != "claude-3" {
		t.Errorf("expected model 'claude-3', got %q", msg.Message.Model)
	}
	if msg.Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Message.Role)
	}
	if msg.Message.Usage == nil || msg.Message.Usage.InputTokens != 100 {
		t.Error("expected usage with 100 input tokens")
	}
}

func TestMapMessageDelta(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:       types.EngineEventMessageDelta,
		StopReason: "end_turn",
		Usage:      &types.Usage{OutputTokens: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg MessageDeltaMessage
	json.Unmarshal(msgs[0], &msg)
	if msg.Type != MsgTypeMessageDelta {
		t.Errorf("expected message.delta, got %s", msg.Type)
	}
	if msg.Delta.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", msg.Delta.StopReason)
	}
	if msg.Usage == nil || msg.Usage.OutputTokens != 50 {
		t.Error("expected usage with 50 output tokens")
	}
}

func TestMapMessageDelta_ClosesOpenTextBlock(t *testing.T) {
	m := NewEventMapper("s1", false)
	// Open a text block.
	_, _ = m.Map(&types.EngineEvent{Type: types.EngineEventText, Text: "hi"})
	// message_delta should close it first.
	msgs, err := m.Map(&types.EngineEvent{
		Type:       types.EngineEventMessageDelta,
		StopReason: "end_turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Expect: content.stop (text) + message.delta
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	var stop ContentStopMessage
	json.Unmarshal(msgs[0], &stop)
	if stop.Type != MsgTypeContentStop {
		t.Errorf("expected content.stop, got %s", stop.Type)
	}
	var delta MessageDeltaMessage
	json.Unmarshal(msgs[1], &delta)
	if delta.Type != MsgTypeMessageDelta {
		t.Errorf("expected message.delta, got %s", delta.Type)
	}
}

func TestMapMessageStop(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{Type: types.EngineEventMessageStop})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg MessageStopMessage
	json.Unmarshal(msgs[0], &msg)
	if msg.Type != MsgTypeMessageStop {
		t.Errorf("expected message.stop, got %s", msg.Type)
	}
	if msg.SessionID != "s1" {
		t.Errorf("expected session_id 's1', got %q", msg.SessionID)
	}
}

func TestMapToolCall(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolCall,
		ToolUseID: "tu_123",
		ToolName:  "bash",
		ToolInput: `{"command":"ls -la"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg ToolCallMessage
	json.Unmarshal(msgs[0], &msg)
	if msg.Type != MsgTypeToolCall {
		t.Errorf("expected tool.call, got %s", msg.Type)
	}
	if msg.ToolUseID != "tu_123" {
		t.Errorf("expected tool_use_id 'tu_123', got %q", msg.ToolUseID)
	}
	if msg.ToolName != "bash" {
		t.Errorf("expected tool_name 'bash', got %q", msg.ToolName)
	}
	if cmd, ok := msg.Input["command"]; !ok || cmd != "ls -la" {
		t.Errorf("expected input.command 'ls -la', got %v", msg.Input)
	}
}

func TestMapToolCall_InvalidJSON(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolCall,
		ToolUseID: "tu_456",
		ToolName:  "bash",
		ToolInput: "not valid json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg ToolCallMessage
	json.Unmarshal(msgs[0], &msg)
	// Invalid JSON should be wrapped in a "raw" key.
	if msg.Input["raw"] != "not valid json" {
		t.Errorf("expected raw input fallback, got %v", msg.Input)
	}
}

// ============================================================
// Full v1.1 event sequence integration test
// ============================================================

func TestIntegration_FullMessageLifecycle(t *testing.T) {
	ch, addr := startTestChannel(t)

	// Mock handler that emits a full v1.1 message lifecycle.
	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		events := []types.EngineEvent{
			{Type: types.EngineEventMessageStart, MessageID: "msg_1", Model: "test-model", Usage: &types.Usage{InputTokens: 10}},
			{Type: types.EngineEventText, Text: "Hello"},
			{Type: types.EngineEventText, Text: " World"},
			{Type: types.EngineEventMessageDelta, StopReason: "end_turn", Usage: &types.Usage{OutputTokens: 5}},
			{Type: types.EngineEventMessageStop},
			{Type: types.EngineEventDone, Terminal: &types.Terminal{Reason: types.TerminalCompleted, Turn: 1}, Usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		}
		for i := range events {
			if err := ch.SendEvent(ctx, msg.SessionID, &events[i]); err != nil {
				return err
			}
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ch.Start(ctx, mockHandler)
	time.Sleep(100 * time.Millisecond)

	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal("dial failed:", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "test done")

	// Send session.create to initialize.
	createMsg, _ := json.Marshal(ClientMessage{
		Type:      MsgTypeSessionCreate,
		SessionID: "lifecycle",
	})
	ws.Write(ctx, websocket.MessageText, createMsg)

	// Read session.created.
	ws.Read(ctx)

	// Send a user.message.
	msg, _ := json.Marshal(ClientMessage{Type: MsgTypeUserMessage, EventID: "evt_test", Text: "hello"})
	ws.Write(ctx, websocket.MessageText, msg)

	// Collect all server events.
	// Expected: message.start → content.start → content.delta → content.delta → content.stop → message.delta → message.stop → task.end
	expectedTypes := []string{
		"message.start",
		"content.start",
		"content.delta",
		"content.delta",
		"content.stop",
		"message.delta",
		"message.stop",
		"task.end",
	}

	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()

	for i, expected := range expectedTypes {
		_, data, err := ws.Read(readCtx)
		if err != nil {
			t.Fatalf("read #%d failed: %v", i, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &envelope)
		if envelope.Type != expected {
			t.Errorf("message %d: expected type %q, got %q", i, expected, envelope.Type)
		}
	}

	cancel()
}

// ============================================================
// Multi-content user.message tests (v1.5)
// ============================================================

func TestContentBlocks_TextShorthand(t *testing.T) {
	// When Content is nil and Text is set, ContentBlocks returns nil.
	msg := ClientMessage{Type: MsgTypeUserMessage, Text: "hello"}
	blocks, err := msg.ContentBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if blocks != nil {
		t.Fatalf("expected nil blocks for text shorthand, got %d", len(blocks))
	}
}

func TestContentBlocks_SingleObject(t *testing.T) {
	// Backward compat: content is a single object.
	raw := json.RawMessage(`{"type":"text","text":"hello world"}`)
	msg := ClientMessage{Type: MsgTypeUserMessage, Content: raw}
	blocks, err := msg.ContentBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "hello world" {
		t.Errorf("unexpected block: %+v", blocks[0])
	}
}

func TestContentBlocks_ArrayMultiType(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"Describe this image"},
		{"type":"image","source":{"type":"path","path":"/tmp/screenshot.png"}},
		{"type":"file","source":{"type":"url","url":"https://example.com/data.csv","media_type":"text/csv"}}
	]`)
	msg := ClientMessage{Type: MsgTypeUserMessage, Content: raw}
	blocks, err := msg.ContentBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// Text block.
	if blocks[0].Type != "text" || blocks[0].Text != "Describe this image" {
		t.Errorf("text block mismatch: %+v", blocks[0])
	}

	// Image block with path source.
	if blocks[1].Type != "image" {
		t.Errorf("expected type 'image', got %q", blocks[1].Type)
	}
	if blocks[1].Source == nil || blocks[1].Source.Type != "path" || blocks[1].Source.Path != "/tmp/screenshot.png" {
		t.Errorf("image source mismatch: %+v", blocks[1].Source)
	}

	// File block with url source.
	if blocks[2].Type != "file" {
		t.Errorf("expected type 'file', got %q", blocks[2].Type)
	}
	if blocks[2].Source == nil || blocks[2].Source.Type != "url" || blocks[2].Source.URL != "https://example.com/data.csv" {
		t.Errorf("file source mismatch: %+v", blocks[2].Source)
	}
	if blocks[2].Source.MediaType != "text/csv" {
		t.Errorf("expected media_type 'text/csv', got %q", blocks[2].Source.MediaType)
	}
}

func TestContentBlocks_ImageBase64(t *testing.T) {
	raw := json.RawMessage(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR..."}}`)
	msg := ClientMessage{Type: MsgTypeUserMessage, Content: raw}
	blocks, err := msg.ContentBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	src := blocks[0].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data != "iVBOR..." {
		t.Errorf("base64 source mismatch: %+v", src)
	}
}

func TestContentBlocks_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid}`)
	msg := ClientMessage{Type: MsgTypeUserMessage, Content: raw}
	_, err := msg.ContentBlocks()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestContentBlocks_EmptyArray(t *testing.T) {
	raw := json.RawMessage(`[]`)
	msg := ClientMessage{Type: MsgTypeUserMessage, Content: raw}
	blocks, err := msg.ContentBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestToIncomingContentBlocks(t *testing.T) {
	blocks := []ClientContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "image", Source: &ClientContentSource{
			Type: "path", Path: "/tmp/img.png", MediaType: "image/png",
		}},
		{Type: "file", Source: &ClientContentSource{
			Type: "url", URL: "https://example.com/f.pdf",
		}},
	}
	result := toIncomingContentBlocks(blocks)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	// text
	if result[0].Type != "text" || result[0].Text != "hello" {
		t.Errorf("text block: %+v", result[0])
	}
	// image
	if result[1].Type != "image" || result[1].Path != "/tmp/img.png" || result[1].MIMEType != "image/png" {
		t.Errorf("image block: %+v", result[1])
	}
	// file
	if result[2].Type != "file" || result[2].URL != "https://example.com/f.pdf" {
		t.Errorf("file block: %+v", result[2])
	}
}

func TestIntegration_MultiContent_UserMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	ln.Close()

	cfg := config.WSChannelConfig{Host: "127.0.0.1", Port: port, Path: "/ws"}

	var received *types.IncomingMessage
	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		received = msg
		return nil
	}

	ch := New(cfg, nil, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ch.Start(ctx, mockHandler)
	time.Sleep(100 * time.Millisecond)

	// Connect.
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")

	// session.create
	initMsg, _ := json.Marshal(ClientMessage{
		Type: MsgTypeSessionCreate, SessionID: "multi-test", UserID: "u1",
	})
	ws.Write(ctx, websocket.MessageText, initMsg)
	ws.Read(ctx) // session.created

	// Send multi-content user.message (array form).
	multiContent := json.RawMessage(`[
		{"type":"text","text":"Look at this image"},
		{"type":"image","source":{"type":"path","path":"/tmp/test.png"}},
		{"type":"file","source":{"type":"url","url":"https://example.com/data.csv","media_type":"text/csv"}}
	]`)
	userMsg, _ := json.Marshal(ClientMessage{
		Type:    MsgTypeUserMessage,
		EventID: "evt_multi",
		Content: multiContent,
	})
	ws.Write(ctx, websocket.MessageText, userMsg)

	// Wait for handler to process.
	time.Sleep(100 * time.Millisecond)

	if received == nil {
		t.Fatal("handler was not called")
	}
	if received.Text != "Look at this image" {
		t.Errorf("expected text 'Look at this image', got %q", received.Text)
	}
	if len(received.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(received.Content))
	}
	if received.Content[0].Type != "text" || received.Content[0].Text != "Look at this image" {
		t.Errorf("content[0] mismatch: %+v", received.Content[0])
	}
	if received.Content[1].Type != "image" || received.Content[1].Path != "/tmp/test.png" {
		t.Errorf("content[1] mismatch: %+v", received.Content[1])
	}
	if received.Content[2].Type != "file" || received.Content[2].URL != "https://example.com/data.csv" {
		t.Errorf("content[2] mismatch: %+v", received.Content[2])
	}
	if received.Content[2].MIMEType != "text/csv" {
		t.Errorf("content[2] mime_type mismatch: %q", received.Content[2].MIMEType)
	}

	cancel()
}