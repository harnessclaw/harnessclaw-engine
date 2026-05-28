package websocket

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/userprompt"
	"harnessclaw-go/internal/engine/wait"
	"harnessclaw-go/internal/storage/sqlite"
	"harnessclaw-go/pkg/types"
)

// recordingResumer captures every Resume call so tests can assert the
// engine-side recovery hook was invoked with the right wait + answer.
type recordingResumer struct {
	mu      sync.Mutex
	calls   []resumeCall
	respond error // returned from Resume; nil = success
}

type resumeCall struct {
	wait   *wait.PendingWait
	answer wait.Answer
}

func (r *recordingResumer) Resume(_ context.Context, w *wait.PendingWait, a wait.Answer) error {
	r.mu.Lock()
	r.calls = append(r.calls, resumeCall{wait: w, answer: a})
	r.mu.Unlock()
	return r.respond
}

func (r *recordingResumer) Calls() []resumeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]resumeCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// startRecoveryChannel boots a Channel wired with a real SQLite-backed
// WaitStore, a Prompter, and the supplied Resumer. Returns the WS URL,
// the Channel (so tests can drive engine events), the underlying *sql.DB
// (so tests can simulate "restart" by spinning up a fresh Channel over
// the same DB), and a recording Resumer.
func startRecoveryChannel(t *testing.T, dbPath string, handler func(ctx context.Context, m *types.IncomingMessage) error) (string, *Channel, *sql.DB, *recordingResumer) {
	t.Helper()
	sessStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = sessStore.Close() })

	ws, err := sqlite.NewWaitStore(sessStore.DB())
	if err != nil {
		t.Fatalf("NewWaitStore: %v", err)
	}
	p := userprompt.New(userprompt.Config{Store: ws})
	rec := &recordingResumer{}

	cfg := config.WSChannelConfig{Host: "127.0.0.1", Port: 0, Path: "/v1/ws"}
	ch := New(cfg, nil, zap.NewNop())
	ch.SetPrompter(p)
	ch.SetResumer(rec)
	ch.translator.SetIssuer(p)

	// Boot the in-process bits Start() would set up.
	ch.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	ch.connCtx = ctx
	ch.connCanc = cancel
	ch.tracker.Start()
	ch.healthy.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Path, ch.upgrade)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		_ = ch.Stop(context.Background())
	})
	return "ws" + strings.TrimPrefix(srv.URL, "http") + cfg.Path, ch, sessStore.DB(), rec
}

// helper: dial and complete session.create handshake.
func dialAndCreate(t *testing.T, url, sessionID string) *websocket.Conn {
	t.Helper()
	ws := dial(t, url)
	send(t, ws, map[string]any{"type": "session.create", "session_id": sessionID})
	_ = recv(t, ws) // session.event(opened)
	return ws
}

// TestRecovery_LiveAnswerStillWorks ensures the recovery wiring doesn't
// regress the in-memory live path. With Prompter set, a wait is also
// persisted, but a live answer must still flow through the in-memory
// translator map → engine handler (NOT through Resumer).
func TestRecovery_LiveAnswerStillWorks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "live.db")
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
	url, ch, _, rec := startRecoveryChannel(t, dbPath, handler)
	ws := dialAndCreate(t, url, "sess_live")

	// Engine emits ask_user_question → translator persists wait + emits prompt.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_live", &types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolName:  "ask_user_question",
			ToolUseID: "toolu_live",
			ToolInput: `{"question":"go?","options":[{"label":"yes"},{"label":"no"}]}`,
		})
	}()

	prompt := recvUntil(t, ws, "prompt.user")
	requestID := prompt["payload"].(map[string]any)["request_id"].(string)

	// User answers immediately — live path.
	send(t, ws, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
		"payload":    map[string]any{"selected_options": []string{"yes"}},
	})

	// Wait for handler to receive tool.result via the live (translator-map) path.
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
		t.Fatalf("got %d tool results, want 1", len(toolResults))
	}
	if toolResults[0].Output != "yes" {
		t.Errorf("output = %q", toolResults[0].Output)
	}
	// Resumer must NOT have fired — live path doesn't go through it.
	if len(rec.Calls()) != 0 {
		t.Errorf("Resumer fired %d times for live path; want 0", len(rec.Calls()))
	}
}

// TestRecovery_ServerRestartReplay is the headline test. Simulates the
// production failure mode:
//   1. Server emits prompt.user → wait persisted to SQLite.
//   2. Server "restarts" (we tear down Channel A, build Channel B over
//      the same DB).
//   3. Client B reconnects with the same session_id.
//   4. Channel B re-emits the persisted prompt to the new connection.
//   5. User answers.
//   6. Channel B's conn.handlePromptResponse misses the in-memory
//      translator map (B's translator is fresh) but finds the wait in
//      SQLite, calls Resumer.Resume.
//   7. Wait is deleted from SQLite (no replay).
func TestRecovery_ServerRestartReplay(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover.db")

	// --- Phase 1: Channel A emits prompt, then we shut it down ---
	urlA, chA, _, _ := startRecoveryChannel(t, dbPath, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	wsA := dialAndCreate(t, urlA, "sess_recover")

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = chA.SendEvent(context.Background(), "sess_recover", &types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolName:  "ask_user_question",
			ToolUseID: "toolu_recover",
			ToolInput: `{"question":"continue?","options":[{"label":"yes"},{"label":"no"}]}`,
		})
	}()

	promptA := recvUntil(t, wsA, "prompt.user")
	requestID := promptA["payload"].(map[string]any)["request_id"].(string)
	t.Logf("Channel A emitted prompt request_id=%s", requestID)

	// Hard kill: close conn, stop channel A. Wait persisted in SQLite
	// because translator persisted-then-emitted. SQLite file remains.
	_ = wsA.Close(websocket.StatusNormalClosure, "")
	_ = chA.Stop(context.Background())

	// --- Phase 2: Channel B over same DB ("restart") ---
	urlB, _, _, recB := startRecoveryChannel(t, dbPath, func(_ context.Context, _ *types.IncomingMessage) error { return nil })

	wsB := dial(t, urlB)
	send(t, wsB, map[string]any{"type": "session.create", "session_id": "sess_recover"})

	// First frame is session.event(opened); next should be the
	// re-emitted prompt.user.
	opened := recv(t, wsB)
	if opened["type"] != "session.event" {
		t.Fatalf("first frame = %v, want session.event(opened)", opened["type"])
	}
	caps := opened["payload"].(map[string]any)["inner"].(map[string]any)["capabilities"].(map[string]any)
	if caps["recovery"] != true {
		t.Errorf("recovery capability not advertised: %v", caps)
	}
	replayedPrompt := recv(t, wsB)
	if replayedPrompt["type"] != "prompt.user" {
		t.Fatalf("expected re-emitted prompt.user; got %v", replayedPrompt["type"])
	}
	replayedRequestID := replayedPrompt["payload"].(map[string]any)["request_id"].(string)
	if replayedRequestID != requestID {
		t.Errorf("re-emitted request_id = %q; want %q (must be same to allow correlation)", replayedRequestID, requestID)
	}

	// --- Phase 3: User answers on the new connection ---
	send(t, wsB, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
		"payload":    map[string]any{"selected_options": []string{"yes"}},
	})

	// --- Phase 4: Resumer must fire ---
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(recB.Calls()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := recB.Calls()
	if len(calls) != 1 {
		t.Fatalf("Resumer fired %d times; want 1 (recovery path broken)", len(calls))
	}
	if calls[0].wait.RequestID != requestID {
		t.Errorf("Resumer received wait %s; want %s", calls[0].wait.RequestID, requestID)
	}
	if calls[0].wait.CorrelationID != "toolu_recover" {
		t.Errorf("Resumer received correlation %q; want toolu_recover", calls[0].wait.CorrelationID)
	}
	if calls[0].wait.Kind != wait.KindQuestion {
		t.Errorf("Resumer received kind %q; want question", calls[0].wait.Kind)
	}
	if calls[0].answer.Decision != "approved" {
		t.Errorf("answer decision = %q", calls[0].answer.Decision)
	}
	if calls[0].answer.Output != "yes" {
		t.Errorf("answer output = %q", calls[0].answer.Output)
	}

	// --- Phase 5: wait must be deleted from SQLite (no replay) ---
	// Simulate: send the same response again — should now hit Path "no
	// match" and produce an error frame, NOT trigger Resumer twice.
	send(t, wsB, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
	})
	got := recv(t, wsB)
	if got["type"] != "session.event" {
		t.Fatalf("duplicate reply should produce error frame; got %v", got["type"])
	}
	pl := got["payload"].(map[string]any)
	if pl["kind"] != "error" {
		t.Errorf("duplicate reply kind = %v, want error", pl["kind"])
	}
	if len(recB.Calls()) != 1 {
		t.Errorf("Resumer fired %d times after duplicate; want still 1 (idempotency)", len(recB.Calls()))
	}
}

// TestRecovery_PlanReviewSurvivesRestart confirms plan_review prompts
// also survive restart — same recovery path, different kind.
func TestRecovery_PlanReviewSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "plan_recover.db")

	urlA, chA, _, _ := startRecoveryChannel(t, dbPath, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	wsA := dialAndCreate(t, urlA, "sess_plan")

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = chA.SendEvent(context.Background(), "sess_plan", &types.EngineEvent{
			Type: types.EngineEventPlanProposed,
			PlanProposal: &types.PlanProposal{
				PlanID: "pln_recover_42",
				Goal:   "调研 X",
				Steps:  []types.ProposedStep{{ID: "s1", Description: "search"}},
			},
		})
	}()
	promptA := recvUntil(t, wsA, "prompt.user")
	requestID := promptA["payload"].(map[string]any)["request_id"].(string)

	_ = wsA.Close(websocket.StatusNormalClosure, "")
	_ = chA.Stop(context.Background())

	urlB, _, _, recB := startRecoveryChannel(t, dbPath, func(_ context.Context, _ *types.IncomingMessage) error { return nil })
	wsB := dial(t, urlB)
	send(t, wsB, map[string]any{"type": "session.create", "session_id": "sess_plan"})
	_ = recv(t, wsB) // opened
	replay := recv(t, wsB)
	if replay["type"] != "prompt.user" {
		t.Fatalf("expected re-emit of plan_review; got %v", replay["type"])
	}

	send(t, wsB, map[string]any{
		"type":       "prompt.user_response",
		"request_id": requestID,
		"decision":   "approved",
		"payload":    map[string]any{"reason": "looks good"},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(recB.Calls()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := recB.Calls()
	if len(calls) != 1 {
		t.Fatalf("Resumer fired %d times for plan_review recovery; want 1", len(calls))
	}
	if calls[0].wait.Kind != wait.KindPlanReview {
		t.Errorf("kind = %q, want plan_review", calls[0].wait.Kind)
	}
	if calls[0].wait.CorrelationID != "pln_recover_42" {
		t.Errorf("correlation = %q, want pln_recover_42 (engine plan_id)", calls[0].wait.CorrelationID)
	}
	if calls[0].answer.Output != "looks good" {
		t.Errorf("output = %q", calls[0].answer.Output)
	}
}

// TestRecovery_PersistFailureSuppressesEmit ensures that if SaveWait
// fails, the prompt.user wire frame is NEVER emitted — preventing the
// "client sees a prompt that server can never recover" hazard.
func TestRecovery_PersistFailureSuppressesEmit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist_fail.db")
	url, ch, _, _ := startRecoveryChannel(t, dbPath, func(_ context.Context, _ *types.IncomingMessage) error { return nil })

	// Replace the issuer with a poisoned one that always errors.
	ch.translator.SetIssuer(failingIssuer{})

	ws := dialAndCreate(t, url, "sess_fail")
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = ch.SendEvent(context.Background(), "sess_fail", &types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolName:  "ask_user_question",
			ToolUseID: "toolu_fail",
			ToolInput: `{"question":"?"}`,
		})
	}()

	// Drain a few frames and assert prompt.user never appears.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, raw, err := ws.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		if strings.Contains(string(raw), `"type":"prompt.user"`) {
			t.Fatalf("prompt.user emitted despite persist failure: %s", string(raw))
		}
	}
}

type failingIssuer struct{}

func (failingIssuer) IssueWait(_ context.Context, _ wait.PendingWait) error {
	return errSimPersistFail
}

var errSimPersistFail = simpleErr("simulated persist failure")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }
