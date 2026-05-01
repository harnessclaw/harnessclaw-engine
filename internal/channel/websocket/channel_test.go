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
				"exit_code":    0,
				"duration_ms":  int64(42),
				"render_hint":  "terminal",
				"command":      "ls -la",
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
	// render_hint should be promoted to top-level.
	if msg.RenderHint != "terminal" {
		t.Errorf("expected render_hint 'terminal', got %q", msg.RenderHint)
	}
	// render_hint should NOT be in residual metadata.
	if msg.Metadata != nil {
		if _, has := msg.Metadata["render_hint"]; has {
			t.Error("render_hint should not be duplicated in metadata")
		}
	}
	// exit_code should remain in metadata.
	if msg.Metadata == nil {
		t.Fatal("expected metadata with exit_code and command")
	}
	ec, ok := msg.Metadata["exit_code"]
	if !ok {
		t.Fatal("metadata missing exit_code")
	}
	// After JSON roundtrip, integer values in map[string]any become float64.
	if ecf, isFloat := ec.(float64); !isFloat || ecf != 0 {
		t.Errorf("expected metadata.exit_code=0 (float64), got %v (%T)", ec, ec)
	}
	// command should remain in metadata (not promoted).
	if _, hasCmd := msg.Metadata["command"]; !hasCmd {
		t.Error("metadata missing command")
	}
}

func TestMapToolEnd_RenderHintPromoted(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolUseID: "tu_2",
		ToolName:  "Read",
		ToolResult: &types.ToolResult{
			Content: "package main",
			Metadata: map[string]any{
				"render_hint": "code",
				"language":    "go",
				"file_path":   "/src/main.go",
				"start_line":  1,
				"lines_read":  10,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var msg ToolEndMessage
	json.Unmarshal(msgs[0], &msg)

	// Promoted fields.
	if msg.RenderHint != "code" {
		t.Errorf("expected render_hint 'code', got %q", msg.RenderHint)
	}
	if msg.Language != "go" {
		t.Errorf("expected language 'go', got %q", msg.Language)
	}
	if msg.FilePath != "/src/main.go" {
		t.Errorf("expected file_path '/src/main.go', got %q", msg.FilePath)
	}

	// Promoted keys must NOT appear in residual metadata.
	if msg.Metadata != nil {
		for _, key := range []string{"render_hint", "language", "file_path"} {
			if _, has := msg.Metadata[key]; has {
				t.Errorf("%s should not be duplicated in metadata", key)
			}
		}
	}

	// Non-promoted keys should remain.
	if msg.Metadata == nil {
		t.Fatal("expected metadata with start_line and lines_read")
	}
	if _, ok := msg.Metadata["start_line"]; !ok {
		t.Error("metadata missing start_line")
	}
	if _, ok := msg.Metadata["lines_read"]; !ok {
		t.Error("metadata missing lines_read")
	}
}

func TestMapToolEnd_NoRenderHint(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolUseID: "tu_3",
		ToolName:  "custom",
		ToolResult: &types.ToolResult{
			Content: "ok",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg ToolEndMessage
	json.Unmarshal(msgs[0], &msg)

	if msg.RenderHint != "" {
		t.Errorf("expected empty render_hint, got %q", msg.RenderHint)
	}
	if msg.Language != "" {
		t.Errorf("expected empty language, got %q", msg.Language)
	}
	if msg.FilePath != "" {
		t.Errorf("expected empty file_path, got %q", msg.FilePath)
	}
}

func TestMapToolEnd_AllPromotedFields(t *testing.T) {
	m := NewEventMapper("s1", false)
	msgs, err := m.Map(&types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolUseID: "tu_4",
		ToolName:  "Edit",
		ToolResult: &types.ToolResult{
			Content: "applied",
			Metadata: map[string]any{
				"duration_ms": int64(100),
				"render_hint": "diff",
				"language":    "python",
				"file_path":   "/app/utils.py",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg ToolEndMessage
	json.Unmarshal(msgs[0], &msg)

	// All four promoted fields should be at top-level.
	if msg.DurationMs != 100 {
		t.Errorf("expected duration_ms 100, got %d", msg.DurationMs)
	}
	if msg.RenderHint != "diff" {
		t.Errorf("expected render_hint 'diff', got %q", msg.RenderHint)
	}
	if msg.Language != "python" {
		t.Errorf("expected language 'python', got %q", msg.Language)
	}
	if msg.FilePath != "/app/utils.py" {
		t.Errorf("expected file_path '/app/utils.py', got %q", msg.FilePath)
	}

	// Metadata should be nil since all keys were promoted.
	if msg.Metadata != nil {
		t.Errorf("expected nil metadata (all keys promoted), got %v", msg.Metadata)
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
	if initMsg.ProtocolVersion != "1.12" {
		t.Errorf("expected protocol_version '1.12', got %q", initMsg.ProtocolVersion)
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
// L3 propagation end-to-end test
// ============================================================
//
// TestIntegration_WebSocket_L3SubAgentChain exercises the full L1→L2→L3
// observability chain on the wire:
//
//   emma (L1, the parent session)
//     └─ Specialists (L2, dispatched as a sync sub-agent — the events below
//                    are what L2's forwarding loop in subagent.go bubbles up
//                    when a deeper L3 sub-agent runs underneath it)
//           └─ researcher (L3, doing actual work)
//
// We feed the channel exactly the event sequence that L2's forwarding loop
// produces (subagent_start / subagent_event / deliverable / subagent_end),
// and assert that the WebSocket client receives the full set of wire frames
// — `subagent.start`, `subagent.event`, `deliverable.ready`, `subagent.end`
// — with the correct AgentID/AgentName/payload fields.
//
// Pre-fix regression: the L2 SpawnSync forwarding loop only matched its own
// ToolStart/ToolEnd, dropping events that originated below it. The unit
// test TestSpawnSync_PassesThroughDeeperLayerEvents in internal/engine
// guards the in-process forwarding; this test guards the wire-format
// translation that follows.
func TestIntegration_WebSocket_L3SubAgentChain(t *testing.T) {
	ch, addr := startTestChannel(t)

	const (
		l3AgentID    = "agent_l3researcher"
		l3AgentName  = "researcher"
		l3ToolUseID  = "tu_l3_websearch"
		l3ToolName   = "WebSearch"
		l3ToolInput  = `{"query":"latest LLM inference papers"}`
		l3ToolOutput = "found 8 results"
		l3FilePath   = "~/.harnessclaw/workspace/deliverables/llm-inference-report.md"
		l3Language   = "markdown"
		l3ByteSize   = 2048
	)

	// The mock handler simulates what happens after L2's forwarding loop:
	// it pushes the sequence of bubbled-up L3 events into the channel as if
	// they had just been forwarded from a deeper sub-agent.
	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		events := []types.EngineEvent{
			// 1) L3 sub-agent starts — Specialists just dispatched it.
			//    AgentTask carries the full prompt so the client can show
			//    "researcher 接到的任务：…" instead of just the short label.
			{
				Type:          types.EngineEventSubAgentStart,
				AgentID:       l3AgentID,
				AgentName:     l3AgentName,
				AgentDesc:     "信息调研专家",
				AgentTask:     "调研大模型推理优化的最新进展，重点关注 vLLM/SGLang/KV-cache 方向",
				AgentType:     "sync",
				ParentAgentID: "agent_specialists_l2",
			},
			// 2) L3 calls a tool — wrapped as subagent.event by the deeper layer.
			{
				Type:      types.EngineEventSubAgentEvent,
				AgentID:   l3AgentID,
				AgentName: l3AgentName,
				SubAgentEvent: &types.SubAgentEventData{
					EventType: "tool_start",
					ToolName:  l3ToolName,
					ToolUseID: l3ToolUseID,
					ToolInput: l3ToolInput,
				},
			},
			// 3) L3's tool completes.
			{
				Type:      types.EngineEventSubAgentEvent,
				AgentID:   l3AgentID,
				AgentName: l3AgentName,
				SubAgentEvent: &types.SubAgentEventData{
					EventType: "tool_end",
					ToolName:  l3ToolName,
					ToolUseID: l3ToolUseID,
					Output:    l3ToolOutput,
				},
			},
			// 4) L3 produced a file — must surface as deliverable.ready so
			//    the client can render/download it.
			{
				Type:      types.EngineEventDeliverable,
				AgentID:   l3AgentID,
				AgentName: l3AgentName,
				Deliverable: &types.Deliverable{
					FilePath: l3FilePath,
					Language: l3Language,
					ByteSize: l3ByteSize,
				},
			},
			// 5) L3 finishes. AgentStatus drives the wire `status` field.
			{
				Type:        types.EngineEventSubAgentEnd,
				AgentID:     l3AgentID,
				AgentName:   l3AgentName,
				AgentStatus: "completed",
				Duration:    1234,
				Usage:       &types.Usage{InputTokens: 200, OutputTokens: 150},
				Terminal:    &types.Terminal{Reason: types.TerminalCompleted, Turn: 4},
			},
			// 6) Top-level done so the client knows this whole turn ended.
			{
				Type: types.EngineEventDone,
				Terminal: &types.Terminal{
					Reason: types.TerminalCompleted,
					Turn:   1,
				},
				Usage: &types.Usage{InputTokens: 250, OutputTokens: 180},
			},
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

	startErr := make(chan error, 1)
	go func() { startErr <- ch.Start(ctx, mockHandler) }()
	time.Sleep(100 * time.Millisecond)

	if err := ch.Health(); err != nil {
		t.Fatal("channel not healthy:", err)
	}

	// Connect WebSocket client.
	wsURL := "ws://" + addr + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal("dial failed:", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "test done")

	// Bootstrap session.
	createMsg, _ := json.Marshal(ClientMessage{
		Type:      MsgTypeSessionCreate,
		SessionID: "l3-chain-session",
	})
	if err := ws.Write(ctx, websocket.MessageText, createMsg); err != nil {
		t.Fatal("write session.create:", err)
	}
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal("read session.created:", err)
	}

	// Trigger the handler.
	userMsg, _ := json.Marshal(ClientMessage{
		Type:    MsgTypeUserMessage,
		EventID: "evt_user_l3",
		Text:    "research latest LLM inference work and write a report",
	})
	if err := ws.Write(ctx, websocket.MessageText, userMsg); err != nil {
		t.Fatal("write user.message:", err)
	}

	// Expected wire frames (one per engine event, since none of these split):
	//   subagent.start, subagent.event, subagent.event, deliverable.ready,
	//   subagent.end, task.end
	const expectedFrameCount = 6
	frames := make([][]byte, 0, expectedFrameCount)
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()
	for i := 0; i < expectedFrameCount; i++ {
		_, data, err := ws.Read(readCtx)
		if err != nil {
			t.Fatalf("read frame #%d failed: %v (got %d frames so far)", i, err, len(frames))
		}
		frames = append(frames, data)
	}

	// Decode the type of each frame for ordering assertion.
	gotTypes := make([]string, len(frames))
	for i, raw := range frames {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("frame %d: invalid JSON: %v\nframe: %s", i, err, string(raw))
		}
		gotTypes[i] = env.Type
	}
	wantTypes := []string{
		string(MsgTypeSubAgentStart),
		string(MsgTypeSubAgentEvent),
		string(MsgTypeSubAgentEvent),
		string(MsgTypeDeliverableReady),
		string(MsgTypeSubAgentEnd),
		string(MsgTypeTaskEnd),
	}
	for i, want := range wantTypes {
		if gotTypes[i] != want {
			t.Errorf("frame %d: got type %q, want %q\nfull sequence: %v",
				i, gotTypes[i], want, gotTypes)
		}
	}

	// --- subagent.start ---
	var startMsg SubAgentStartMessage
	if err := json.Unmarshal(frames[0], &startMsg); err != nil {
		t.Fatalf("decode subagent.start: %v", err)
	}
	if startMsg.AgentID != l3AgentID {
		t.Errorf("subagent.start agent_id: got %q, want %q", startMsg.AgentID, l3AgentID)
	}
	if startMsg.AgentName != l3AgentName {
		t.Errorf("subagent.start agent_name: got %q, want %q", startMsg.AgentName, l3AgentName)
	}
	if startMsg.AgentType != "sync" {
		t.Errorf("subagent.start agent_type: got %q, want %q", startMsg.AgentType, "sync")
	}
	if startMsg.ParentAgentID != "agent_specialists_l2" {
		t.Errorf("subagent.start parent_agent_id should preserve L2 chain, got %q", startMsg.ParentAgentID)
	}
	wantTask := "调研大模型推理优化的最新进展，重点关注 vLLM/SGLang/KV-cache 方向"
	if startMsg.Task != wantTask {
		t.Errorf("subagent.start task: got %q\nwant %q", startMsg.Task, wantTask)
	}
	if startMsg.SessionID != "l3-chain-session" {
		t.Errorf("subagent.start session_id: got %q", startMsg.SessionID)
	}

	// --- subagent.event (tool_start) ---
	var toolStart SubAgentEventMessage
	if err := json.Unmarshal(frames[1], &toolStart); err != nil {
		t.Fatalf("decode subagent.event[tool_start]: %v", err)
	}
	if toolStart.AgentID != l3AgentID {
		t.Errorf("subagent.event[tool_start] agent_id: got %q, want %q", toolStart.AgentID, l3AgentID)
	}
	if toolStart.Payload == nil {
		t.Fatal("subagent.event[tool_start] payload is nil")
	}
	if toolStart.Payload.EventType != "tool_start" {
		t.Errorf("payload.event_type: got %q, want tool_start", toolStart.Payload.EventType)
	}
	if toolStart.Payload.ToolName != l3ToolName {
		t.Errorf("payload.tool_name: got %q, want %q", toolStart.Payload.ToolName, l3ToolName)
	}
	if toolStart.Payload.ToolUseID != l3ToolUseID {
		t.Errorf("payload.tool_use_id: got %q, want %q", toolStart.Payload.ToolUseID, l3ToolUseID)
	}
	if toolStart.Payload.ToolInput != l3ToolInput {
		t.Errorf("payload.tool_input: got %q, want %q", toolStart.Payload.ToolInput, l3ToolInput)
	}

	// --- subagent.event (tool_end) ---
	var toolEnd SubAgentEventMessage
	if err := json.Unmarshal(frames[2], &toolEnd); err != nil {
		t.Fatalf("decode subagent.event[tool_end]: %v", err)
	}
	if toolEnd.Payload == nil || toolEnd.Payload.EventType != "tool_end" {
		t.Errorf("expected tool_end payload, got %+v", toolEnd.Payload)
	}
	if toolEnd.Payload != nil && toolEnd.Payload.Output != l3ToolOutput {
		t.Errorf("tool_end output: got %q, want %q", toolEnd.Payload.Output, l3ToolOutput)
	}

	// --- deliverable.ready ---
	var deliverable DeliverableReadyMessage
	if err := json.Unmarshal(frames[3], &deliverable); err != nil {
		t.Fatalf("decode deliverable.ready: %v", err)
	}
	if deliverable.AgentID != l3AgentID {
		t.Errorf("deliverable agent_id: got %q, want %q", deliverable.AgentID, l3AgentID)
	}
	if deliverable.AgentName != l3AgentName {
		t.Errorf("deliverable agent_name: got %q, want %q", deliverable.AgentName, l3AgentName)
	}
	if deliverable.FilePath != l3FilePath {
		t.Errorf("deliverable file_path: got %q, want %q", deliverable.FilePath, l3FilePath)
	}
	if deliverable.Language != l3Language {
		t.Errorf("deliverable language: got %q, want %q", deliverable.Language, l3Language)
	}
	if deliverable.ByteSize != l3ByteSize {
		t.Errorf("deliverable byte_size: got %d, want %d", deliverable.ByteSize, l3ByteSize)
	}

	// --- subagent.end ---
	var endMsg SubAgentEndMessage
	if err := json.Unmarshal(frames[4], &endMsg); err != nil {
		t.Fatalf("decode subagent.end: %v", err)
	}
	if endMsg.AgentID != l3AgentID {
		t.Errorf("subagent.end agent_id: got %q, want %q", endMsg.AgentID, l3AgentID)
	}
	if endMsg.Status != "completed" {
		t.Errorf("subagent.end status: got %q, want completed", endMsg.Status)
	}
	if endMsg.DurationMs != 1234 {
		t.Errorf("subagent.end duration_ms: got %d, want 1234", endMsg.DurationMs)
	}
	if endMsg.NumTurns != 4 {
		t.Errorf("subagent.end num_turns: got %d, want 4", endMsg.NumTurns)
	}
	if endMsg.Usage == nil {
		t.Fatal("subagent.end usage is nil")
	}
	if endMsg.Usage.InputTokens != 200 || endMsg.Usage.OutputTokens != 150 {
		t.Errorf("subagent.end usage: got %+v, want input=200/output=150", endMsg.Usage)
	}

	// --- task.end (top-level turn end) ---
	var taskEnd TaskEndMessage
	if err := json.Unmarshal(frames[5], &taskEnd); err != nil {
		t.Fatalf("decode task.end: %v", err)
	}
	if taskEnd.Status != "success" {
		t.Errorf("task.end status: got %q, want success", taskEnd.Status)
	}

	// Sanity: every frame must carry the originating session_id so the client
	// can route correctly when multiplexed.
	for i, raw := range frames {
		var env struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &env)
		if env.SessionID != "l3-chain-session" {
			t.Errorf("frame %d (%s): session_id %q, want l3-chain-session",
				i, gotTypes[i], env.SessionID)
		}
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
// Model error event sequence integration test
// ============================================================

// TestIntegration_ModelError_AllFrames verifies that when a model error occurs,
// the client receives every frame: message.start, error, message.delta (with
// error detail), message.stop, and task.end (with error message).
func TestIntegration_ModelError_AllFrames(t *testing.T) {
	ch, addr := startTestChannel(t)

	modelErr := fmt.Errorf("bifrost: stream request failed: provider returned non-SSE response")

	// Simulate the exact event sequence that queryloop emits on Chat() failure:
	//   message.start → error → message.delta(stop_reason=error) → message.stop → done
	mockHandler := func(ctx context.Context, msg *types.IncomingMessage) error {
		events := []types.EngineEvent{
			{Type: types.EngineEventMessageStart, MessageID: "msg_err1", Model: "test-model", Usage: &types.Usage{InputTokens: 10}},
			{Type: types.EngineEventError, Error: modelErr},
			{Type: types.EngineEventMessageDelta, StopReason: "error", Error: modelErr},
			{Type: types.EngineEventMessageStop},
			{Type: types.EngineEventDone, Terminal: &types.Terminal{
				Reason:  types.TerminalModelError,
				Message: modelErr.Error(),
				Turn:    1,
			}, Usage: &types.Usage{InputTokens: 10}},
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

	// Initialize session.
	createMsg, _ := json.Marshal(ClientMessage{Type: MsgTypeSessionCreate, SessionID: "err-test"})
	ws.Write(ctx, websocket.MessageText, createMsg)
	ws.Read(ctx) // consume session.created

	// Send user message to trigger the error flow.
	msg, _ := json.Marshal(ClientMessage{Type: MsgTypeUserMessage, Text: "trigger error"})
	ws.Write(ctx, websocket.MessageText, msg)

	// Expected frame sequence.
	expectedTypes := []string{
		"message.start",
		"error",
		"message.delta",
		"message.stop",
		"task.end",
	}

	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()

	frames := make([]json.RawMessage, len(expectedTypes))
	for i := range expectedTypes {
		_, data, err := ws.Read(readCtx)
		if err != nil {
			t.Fatalf("read frame #%d failed: %v", i, err)
		}
		frames[i] = data
		var envelope struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &envelope)
		if envelope.Type != expectedTypes[i] {
			t.Errorf("frame %d: expected type %q, got %q (body: %s)", i, expectedTypes[i], envelope.Type, string(data))
		}
	}

	// Verify the error frame (index 1) has the error message.
	var errFrame ErrorMessage
	json.Unmarshal(frames[1], &errFrame)
	if errFrame.Error.Message == "" {
		t.Error("error frame: expected non-empty error.message")
	}
	if errFrame.Error.Code != "engine_error" {
		t.Errorf("error frame: expected code 'engine_error', got %q", errFrame.Error.Code)
	}
	t.Logf("error frame: %s", string(frames[1]))

	// Verify message.delta (index 2) contains error detail.
	var deltaFrame MessageDeltaMessage
	json.Unmarshal(frames[2], &deltaFrame)
	if deltaFrame.Delta.StopReason != "error" {
		t.Errorf("message.delta: expected stop_reason 'error', got %q", deltaFrame.Delta.StopReason)
	}
	if deltaFrame.Delta.Error == nil {
		t.Error("message.delta: expected delta.error to be present")
	} else if deltaFrame.Delta.Error.Message == "" {
		t.Error("message.delta: expected non-empty delta.error.message")
	} else {
		t.Logf("message.delta error: %s", deltaFrame.Delta.Error.Message)
	}
	t.Logf("message.delta frame: %s", string(frames[2]))

	// Verify task.end (index 4) contains the error message.
	var taskEnd TaskEndMessage
	json.Unmarshal(frames[4], &taskEnd)
	if taskEnd.Status != "error_model" {
		t.Errorf("task.end: expected status 'error_model', got %q", taskEnd.Status)
	}
	if taskEnd.Message == "" {
		t.Error("task.end: expected non-empty message")
	}
	t.Logf("task.end frame: %s", string(frames[4]))

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
