package emma

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/commands"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/tools"
)

// newAbortTestEngine builds a minimal *emma.Engine wired with a real
// session manager and a single test session. The provider stays a no-op
// mock because none of the abort tests touch the LLM path — they only
// exercise the cancel-map and Awaits coupling that AbortSession owns.
func newAbortTestEngine(t *testing.T) (*Engine, *session.Session) {
	t.Helper()
	store := memory.New()
	logger := zap.NewNop()
	mgr := session.NewManager(store, logger, time.Hour)
	sess, err := mgr.GetOrCreate(context.Background(), "test_sid", "ws", "user_1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	cfg := DefaultConfig()
	cfg.ToolTimeout = time.Second
	cfg.SystemPrompt = ""

	eng := New(
		&emmaMockProvider{},
		tool.NewRegistry(),
		mgr,
		nil, // compactor
		permission.BypassChecker{},
		logger,
		cfg,
		command.NewRegistry(),
	)
	return eng, sess
}

// TestAbortSession_UnblocksPendingAwaits asserts that aborting a session
// closes any pending ToolAwait result channels promptly, so callers
// blocked on aw.Result unblock without waiting on the slower ctx.Done()
// path.
func TestAbortSession_UnblocksPendingAwaits(t *testing.T) {
	eng, sess := newAbortTestEngine(t)

	// Simulate an active query by registering a cancel function — the
	// same call ProcessMessage makes when a query starts.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.RegisterCancel(sess.ID, cancel)

	// Push a pending tool await.
	aw := sess.Awaits.PushTool("use_1", "Read")

	// Abort the session.
	if err := eng.AbortSession(context.Background(), sess.ID); err != nil {
		t.Fatalf("AbortSession: %v", err)
	}

	// Verify ToolAwait result channel closes within 10ms.
	select {
	case _, ok := <-aw.Result:
		if ok {
			t.Error("Result delivered a value; want closed channel")
		}
	case <-time.After(10 * time.Millisecond):
		t.Fatal("Result not closed within 10ms after AbortSession")
	}
}

// TestAbortSession_NoActiveQuery asserts that aborting an unknown session
// returns an error rather than silently succeeding.
func TestAbortSession_NoActiveQuery(t *testing.T) {
	eng, _ := newAbortTestEngine(t)

	err := eng.AbortSession(context.Background(), "no_such_session")
	if err == nil {
		t.Error("AbortSession on unknown session: got nil, want error")
	}
}

// TestAbortSession_DeletesFromCancels asserts that AbortSession removes
// the session's cancel registration so a second abort returns the
// "no active query" error path. This is observable via the public API
// without peeking at the cancels map.
func TestAbortSession_DeletesFromCancels(t *testing.T) {
	eng, sess := newAbortTestEngine(t)

	// Register a cancel function.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.RegisterCancel(sess.ID, cancel)

	// First abort succeeds.
	if err := eng.AbortSession(context.Background(), sess.ID); err != nil {
		t.Fatalf("AbortSession: %v", err)
	}

	// Second abort returns "no active query" — proves the cancel
	// registration was deleted by the first call.
	err := eng.AbortSession(context.Background(), sess.ID)
	if err == nil {
		t.Error("second AbortSession should return error")
	}
}
