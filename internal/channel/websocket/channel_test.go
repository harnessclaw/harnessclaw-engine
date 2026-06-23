package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// startTestChannel boots a Channel in front of httptest server. Returns
// the WS URL and a cleanup hook. Internally it bypasses the public
// Start() (which blocks) and uses the same underlying machinery.
func startTestChannel(t *testing.T, handler func(ctx context.Context, msg *types.IncomingMessage) error) (wsURL string, ch *Channel) {
	return startTestChannelWithAbort(t, handler, nil)
}

// startTestChannelWithAbort 是 startTestChannel 的扩展版本 —— 测试 session.interrupt
// 路径时把 abortFn 串进来观测调用。abortFn nil 时与原版完全等价。
func startTestChannelWithAbort(
	t *testing.T,
	handler func(ctx context.Context, msg *types.IncomingMessage) error,
	abortFn func(context.Context, string) error,
) (wsURL string, ch *Channel) {
	t.Helper()
	cfg := config.WSChannelConfig{Host: "127.0.0.1", Port: 0, Path: "/v1/ws"}
	logger := zap.NewNop()
	ch = New(cfg, abortFn, logger)

	// Initialise the bits Start() would set up, minus the blocking listener.
	ctx, cancel := context.WithCancel(context.Background())
	ch.connCtx = ctx
	ch.connCanc = cancel
	ch.tracker.Start()
	ch.healthy.Store(true)

	// Stand-in for the old push-style handler: spawn a goroutine that
	// drains messages and invokes handler, mimicking the router's
	// role, so existing tests don't need to change.
	go func() {
		for msg := range ch.messages {
			_ = handler(ctx, msg)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Path, ch.upgrade)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		_ = ch.Close()
	})
	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http") + cfg.Path
	return wsURL, ch
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	return ws
}

func send(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func recv(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, string(raw))
	}
	return m
}

func TestChannel_HandshakeOpenedFrame(t *testing.T) {
	url, _ := startTestChannel(t, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	ws := dial(t, url)

	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_1"})
	got := recv(t, ws)

	if got["type"] != "session.event" {
		t.Fatalf("first frame type = %v, want session.event", got["type"])
	}
	pl := got["payload"].(map[string]any)
	if pl["kind"] != "opened" {
		t.Fatalf("payload.kind = %v, want opened", pl["kind"])
	}
}

func TestChannel_PreInitGate_RejectsUserMessage(t *testing.T) {
	url, _ := startTestChannel(t, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "user.message", "text": "hi"})
	got := recv(t, ws)
	pl := got["payload"].(map[string]any)
	if pl["kind"] != "error" {
		t.Errorf("expected pre-init error frame; got kind=%v", pl["kind"])
	}
}

func TestChannel_UserMessageDispatchedToEngine(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  int
		gotMsg *types.IncomingMessage
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		mu.Lock()
		calls++
		gotMsg = m
		mu.Unlock()
		return nil
	}
	url, _ := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_dispatch"})
	_ = recv(t, ws) // opened
	send(t, ws, map[string]any{"type": "user.message", "text": "hello world", "coordinator_mode": "react"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := calls
		mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("handler invoked %d times, want 1", calls)
	}
	if gotMsg.Text != "hello world" {
		t.Errorf("text = %q", gotMsg.Text)
	}
	if gotMsg.SessionID != "sess_dispatch" {
		t.Errorf("session = %q", gotMsg.SessionID)
	}
	if gotMsg.CoordinatorMode != "react" {
		t.Errorf("coordinator_mode = %q", gotMsg.CoordinatorMode)
	}
}

func TestChannel_SendEventTranslatesAndStreamsCards(t *testing.T) {
	url, ch := startTestChannel(t, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_stream"})
	_ = recv(t, ws) // opened

	// Drive the engine-style API as the real engine would.
	go func() {
		// Slight delay so the conn is fully registered.
		time.Sleep(50 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_stream",
			&types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
		_ = ch.SendEvent(context.Background(), "sess_stream",
			&types.EngineEvent{Type: types.EngineEventText, Text: "Hello"})
		_ = ch.SendEvent(context.Background(), "sess_stream",
			&types.EngineEvent{Type: types.EngineEventMessageStop})
		_ = ch.SendEvent(context.Background(), "sess_stream",
			&types.EngineEvent{Type: types.EngineEventDone, Terminal: &types.Terminal{Reason: types.TerminalCompleted, Turn: 1}})
	}()

	// Expect: turn add, message add, append, message close, turn close — at minimum.
	seen := []string{}
	for i := 0; i < 5; i++ {
		ev := recv(t, ws)
		seen = append(seen, ev["type"].(string))
	}
	want := []string{"card.add", "card.add", "card.append", "card.close", "card.close"}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("event #%d = %s, want %s (full=%v)", i, seen[i], w, seen)
		}
	}
}

func TestChannel_NameAndHealth(t *testing.T) {
	cfg := config.WSChannelConfig{Host: "127.0.0.1", Port: 0, Path: "/v1/ws"}
	ch := New(cfg, nil, zap.NewNop())
	if ch.Name() != "websocket" {
		t.Errorf("Name = %q", ch.Name())
	}
	if err := ch.Health(); err == nil {
		t.Error("Health should return error before Start")
	}
	ch.healthy.Store(true)
	if err := ch.Health(); err != nil {
		t.Errorf("Health after healthy=true: %v", err)
	}
}

// TestChannel_AskUserQuestionRoundTrip verifies that ask_user_question is
// upgraded to prompt.user(kind=question) on the wire AND that the
// engine's tool-result wait mechanism still works: the user's
// prompt.user_response is bridged back to a tool.result IncomingMessage,
// which is what the engine's askUserQuestion tool blocks on.
func TestChannel_AskUserQuestionRoundTrip(t *testing.T) {
	var (
		mu          sync.Mutex
		toolResults []*types.ToolResultPayload
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.ToolResult != nil {
			mu.Lock()
			toolResults = append(toolResults, m.ToolResult)
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_aq"})
	_ = recv(t, ws) // opened

	// Engine emits a client-tool call for ask_user_question. Translator
	// must upgrade to prompt.user(kind=question), NOT card.add(tool).
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_aq", &types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolName:  "ask_user_question",
			ToolUseID: "toolu_q1",
			ToolInput: `{"question":"Pick a color","options":[{"label":"red"},{"label":"blue"}],"allow_custom":true}`,
		})
	}()

	// Translator emits a card.add(turn) first as a side-effect of
	// openTurnIfNeeded; drain until prompt.user appears.
	var got map[string]any
	for i := 0; i < 5; i++ {
		got = recv(t, ws)
		if got["type"] == "prompt.user" {
			break
		}
	}
	if got["type"] != "prompt.user" {
		t.Fatalf("expected prompt.user (ask_user_question upgraded); got type=%v", got["type"])
	}
	pl := got["payload"].(map[string]any)
	if pl["kind"] != "question" {
		t.Errorf("payload.kind = %v, want question", pl["kind"])
	}
	requestID := pl["request_id"].(string)
	inner := pl["inner"].(map[string]any)
	if inner["question"] != "Pick a color" {
		t.Errorf("question = %v", inner["question"])
	}

	// User responds via prompt.user_response — bridge converts to tool.result.
	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
		"payload":    map[string]any{"selected_options": []string{"red"}},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(toolResults)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(toolResults) != 1 {
		t.Fatalf("engine got %d tool.results, want 1 (the wait mechanism is broken)", len(toolResults))
	}
	r := toolResults[0]
	if r.ToolUseID != "toolu_q1" {
		t.Errorf("tool_use_id = %q, want toolu_q1", r.ToolUseID)
	}
	if r.Status != "success" {
		t.Errorf("status = %q, want success", r.Status)
	}
	if r.Output != "red" {
		t.Errorf("output = %q, want red (the user's selected option)", r.Output)
	}
}

// awaitPlanResponses spins until handler has captured n PlanResponses or
// timeout (2s) elapses. Returns the captured slice (length n on success).
func awaitPlanResponses(t *testing.T, mu *sync.Mutex, sink *[]*types.PlanResponse, n int) []*types.PlanResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*sink)
		mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]*types.PlanResponse, len(*sink))
	copy(out, *sink)
	return out
}

// recvUntil reads frames until one with type==want appears (or 5 tries).
func recvUntil(t *testing.T, ws *websocket.Conn, want string) map[string]any {
	t.Helper()
	for i := 0; i < 5; i++ {
		f := recv(t, ws)
		if f["type"] == want {
			return f
		}
	}
	t.Fatalf("never saw frame type=%s", want)
	return nil
}

// TestChannel_PlanReviewApproveAsIs verifies the plan_review round-trip
// when the user approves the proposed plan unchanged. Critical: the
// engine PlanCoordinator is keyed on plan_id (not the v2.2 request_id),
// so the bridged PlanResponse MUST carry the engine's plan_id or
// PlanCoordinator hangs forever.
func TestChannel_PlanReviewApproveAsIs(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*types.PlanResponse
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.PlanResponse != nil {
			mu.Lock()
			got = append(got, m.PlanResponse)
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_plan_ok"})
	_ = recv(t, ws) // opened

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_plan_ok", &types.EngineEvent{
			Type: types.EngineEventPlanProposed,
			PlanProposal: &types.PlanProposal{
				PlanID: "pln_engine_42",
				Goal:   "调研 X",
				Steps: []types.ProposedStep{
					{ID: "s1", Description: "调研"},
				},
			},
		})
	}()

	prompt := recvUntil(t, ws, "prompt.user")
	pl := prompt["payload"].(map[string]any)
	if pl["kind"] != "plan_review" {
		t.Fatalf("payload.kind = %v, want plan_review", pl["kind"])
	}
	wireRequestID := pl["request_id"].(string)
	if wireRequestID == "pln_engine_42" {
		t.Fatal("wire request_id should NOT be the engine plan_id (those are independent namespaces)")
	}

	// User approves with empty payload (no edits).
	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": wireRequestID,
		"decision":   "approved",
	})

	resps := awaitPlanResponses(t, &mu, &got, 1)
	if len(resps) != 1 {
		t.Fatalf("engine got %d PlanResponses, want 1 (PlanCoordinator would hang)", len(resps))
	}
	if resps[0].PlanID != "pln_engine_42" {
		t.Errorf("PlanResponse.PlanID = %q, want pln_engine_42 (engine-side ID, not wire request_id %q)",
			resps[0].PlanID, wireRequestID)
	}
	if !resps[0].Approved {
		t.Error("Approved = false; expected true")
	}
}

// TestChannel_PlanReviewWithEdits verifies the user-edited steps reach
// the engine intact and the plan_id is still correctly bridged.
func TestChannel_PlanReviewWithEdits(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*types.PlanResponse
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.PlanResponse != nil {
			mu.Lock()
			got = append(got, m.PlanResponse)
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_plan_edit"})
	_ = recv(t, ws)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_plan_edit", &types.EngineEvent{
			Type: types.EngineEventPlanProposed,
			PlanProposal: &types.PlanProposal{
				PlanID: "pln_orig",
				Steps:  []types.ProposedStep{{ID: "s1"}},
			},
		})
	}()

	prompt := recvUntil(t, ws, "prompt.user")
	requestID := prompt["payload"].(map[string]any)["request_id"].(string)

	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
		"payload": map[string]any{
			"updated_steps": []map[string]any{
				{"id": "s1", "description": "edited research"},
				{"id": "s2", "description": "additional summarisation step"},
			},
			"reason": "added missing summarisation",
		},
	})

	resps := awaitPlanResponses(t, &mu, &got, 1)
	if len(resps) != 1 {
		t.Fatalf("got %d PlanResponses", len(resps))
	}
	r := resps[0]
	if r.PlanID != "pln_orig" {
		t.Errorf("plan_id = %q, want pln_orig", r.PlanID)
	}
	if len(r.UpdatedSteps) != 2 {
		t.Errorf("UpdatedSteps len = %d, want 2", len(r.UpdatedSteps))
	}
	if r.Reason != "added missing summarisation" {
		t.Errorf("Reason = %q", r.Reason)
	}
}

// TestChannel_PlanReviewRejected verifies decision=denied still reaches
// the engine so PlanCoordinator can take its degraded path (rather than
// hanging waiting for an approval that never comes).
func TestChannel_PlanReviewRejected(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*types.PlanResponse
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.PlanResponse != nil {
			mu.Lock()
			got = append(got, m.PlanResponse)
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_plan_rej"})
	_ = recv(t, ws)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_plan_rej", &types.EngineEvent{
			Type:         types.EngineEventPlanProposed,
			PlanProposal: &types.PlanProposal{PlanID: "pln_no", Steps: []types.ProposedStep{{ID: "s1"}}},
		})
	}()

	prompt := recvUntil(t, ws, "prompt.user")
	requestID := prompt["payload"].(map[string]any)["request_id"].(string)

	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "denied",
		"payload":    map[string]any{"reason": "scope wrong"},
	})

	resps := awaitPlanResponses(t, &mu, &got, 1)
	if len(resps) != 1 {
		t.Fatalf("got %d PlanResponses (engine wouldn't unblock)", len(resps))
	}
	if resps[0].Approved {
		t.Error("Approved = true; expected false")
	}
	if resps[0].PlanID != "pln_no" {
		t.Errorf("plan_id = %q", resps[0].PlanID)
	}
}

// TestChannel_PermissionRequestRoundTrip verifies the permission round-
// trip carries the engine-side PermissionRequest.RequestID (e.g.
// "perm_xxx") to the engine, NOT the v2.2 wire request_id. Without
// this mapping the engine tool executor's pending-permissions map
// can't match the response and the tool call hangs.
func TestChannel_PermissionRequestRoundTrip(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*types.PermissionResponse
	)
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.PermissionResponse != nil {
			mu.Lock()
			got = append(got, m.PermissionResponse)
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_perm"})
	_ = recv(t, ws)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_perm", &types.EngineEvent{
			Type: types.EngineEventPermissionRequest,
			PermissionRequest: &types.PermissionRequest{
				RequestID: "perm_engine_99",
				ToolName:  "bash",
				ToolInput: "rm -rf /tmp/x",
				Message:   "Allow shell?",
				Options: []types.PermissionOption{
					{Label: "Allow once", Scope: types.PermissionScopeOnce, Allow: true},
					{Label: "Deny", Scope: types.PermissionScopeOnce, Allow: false},
				},
			},
		})
	}()

	prompt := recvUntil(t, ws, "prompt.user")
	pl := prompt["payload"].(map[string]any)
	if pl["kind"] != "permission" {
		t.Fatalf("payload.kind = %v, want permission", pl["kind"])
	}
	wireRequestID := pl["request_id"].(string)
	if wireRequestID == "perm_engine_99" {
		t.Fatal("wire request_id should NOT be the engine permission ID (independent namespaces)")
	}

	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": wireRequestID,
		"decision":   "approved",
		"payload":    map[string]any{"scope": "session"},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("engine got %d PermissionResponses, want 1 (tool would hang)", len(got))
	}
	r := got[0]
	if r.RequestID != "perm_engine_99" {
		t.Errorf("RequestID = %q, want perm_engine_99 (engine ID, not wire request_id %q)",
			r.RequestID, wireRequestID)
	}
	if !r.Approved {
		t.Error("Approved = false; expected true")
	}
	if r.Scope != types.PermissionScopeSession {
		t.Errorf("Scope = %q, want session", r.Scope)
	}
}

// TestChannel_PromptResponseUnknownRequestID verifies a stray response
// carrying an unrecognised request_id is rejected with an error frame
// rather than silently misrouted.
func TestChannel_PromptResponseUnknownRequestID(t *testing.T) {
	url, _ := startTestChannel(t, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_stray"})
	_ = recv(t, ws)

	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": "req_never_sent",
		"decision":   "approved",
	})

	got := recv(t, ws)
	if got["type"] != "session.event" {
		t.Fatalf("expected error frame; got %v", got["type"])
	}
	pl := got["payload"].(map[string]any)
	if pl["kind"] != "error" {
		t.Errorf("payload.kind = %v, want error", pl["kind"])
	}
}

func TestChannel_ToolResultUsesFrameSessionID(t *testing.T) {
	var gotSessionID string
	var got *types.ToolResultPayload
	var mu sync.Mutex
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.ToolResult != nil {
			mu.Lock()
			gotSessionID = m.SessionID
			got = m.ToolResult
			mu.Unlock()
		}
		return nil
	}
	url, _ := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_display"})
	_ = recv(t, ws)

	send(t, ws, map[string]any{
		"type":        "tool.result",
		"session_id":  "sess_await",
		"tool_use_id": "tooluse_browser",
		"status":      "success",
		"output":      "ok",
		"metadata": map[string]any{
			"session_id":                 "browser_session_123",
			"active_tab_id":              "tab_1",
			"agent_browser_session_name": "harnessclaw-browser-browser_session_123",
			"cdp_endpoint":               "ws://127.0.0.1:9222/devtools/page/1",
		},
	})

	deadline := time.After(time.Second)
	for {
		mu.Lock()
		ready := got != nil
		sessionID := gotSessionID
		mu.Unlock()
		if ready {
			if sessionID != "sess_await" {
				t.Fatalf("IncomingMessage.SessionID = %q, want sess_await", sessionID)
			}
			if got.Metadata["cdp_endpoint"] != "ws://127.0.0.1:9222/devtools/page/1" {
				t.Fatalf("tool result metadata not preserved: %#v", got.Metadata)
			}
			if got.Metadata["agent_browser_session_name"] != "harnessclaw-browser-browser_session_123" {
				t.Fatalf("tool result session metadata not preserved: %#v", got.Metadata)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for tool.result handler")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestChannel_AskUserQuestionCancelled verifies "decision=denied" maps to
// status=cancelled in the bridged tool.result.
func TestChannel_AskUserQuestionCancelled(t *testing.T) {
	var got *types.ToolResultPayload
	var mu sync.Mutex
	handler := func(_ context.Context, m *types.IncomingMessage) error {
		if m.ToolResult != nil {
			mu.Lock()
			got = m.ToolResult
			mu.Unlock()
		}
		return nil
	}
	url, ch := startTestChannel(t, handler)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_cancel"})
	_ = recv(t, ws)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_cancel", &types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolName:  "ask_user_question",
			ToolUseID: "toolu_q2",
			ToolInput: `{"question":"go ahead?"}`,
		})
	}()
	var prompt map[string]any
	for i := 0; i < 5; i++ {
		prompt = recv(t, ws)
		if prompt["type"] == "prompt.user" {
			break
		}
	}
	if prompt["type"] != "prompt.user" {
		t.Fatalf("never got prompt.user; last frame = %v", prompt)
	}
	requestID := prompt["payload"].(map[string]any)["request_id"].(string)

	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "denied",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := got != nil
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatalf("no tool.result delivered")
	}
	if got.Status != "cancelled" {
		t.Errorf("cancelled status = %q, want cancelled", got.Status)
	}
}

// TestChannel_SessionInterruptInvokesAbortFn verifies that a client-sent
// session.interrupt frame triggers the injected abortFn for the current
// session — exercising the wiring fix for the "cancel did nothing" bug.
func TestChannel_SessionInterruptInvokesAbortFn(t *testing.T) {
	var (
		mu        sync.Mutex
		gotCalls  []string
		releaseCh = make(chan struct{})
	)
	abortFn := func(_ context.Context, sid string) error {
		mu.Lock()
		gotCalls = append(gotCalls, sid)
		mu.Unlock()
		select {
		case releaseCh <- struct{}{}:
		default:
		}
		return nil
	}

	url, _ := startTestChannelWithAbort(t,
		func(_ context.Context, _ *types.IncomingMessage) error { return nil },
		abortFn,
	)
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_interrupt"})
	_ = recv(t, ws) // session.event opened

	send(t, ws, map[string]any{"type": "session.interrupt"})

	select {
	case <-releaseCh:
	case <-time.After(2 * time.Second):
		t.Fatal("abortFn was not invoked within 2s of session.interrupt")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotCalls) != 1 {
		t.Fatalf("abortFn calls = %d, want 1", len(gotCalls))
	}
	if gotCalls[0] != "sess_interrupt" {
		t.Errorf("abortFn sessionID = %q, want %q", gotCalls[0], "sess_interrupt")
	}
}

// TestChannel_SessionInterruptNilAbortFn ensures the legacy no-abortFn
// path (e.g. tests, channels not wired to engine) still degrades to
// log-only without crashing.
func TestChannel_SessionInterruptNilAbortFn(t *testing.T) {
	url, _ := startTestChannel(t, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": "sess_no_abort"})
	_ = recv(t, ws)

	// Should NOT crash / disconnect when abortFn is nil
	send(t, ws, map[string]any{"type": "session.interrupt"})

	// Subsequent ping should still work — connection is alive
	send(t, ws, map[string]any{"type": "ping"})
	pong := recv(t, ws)
	if pong["type"] != "pong" {
		t.Errorf("after interrupt with nil abortFn, ping/pong broken: %v", pong)
	}
}
