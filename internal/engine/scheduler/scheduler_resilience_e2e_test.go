// Package scheduler_test — resilience e2e tests (Tasks 48-49).
package scheduler_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
)

// ─── Task 48: tstate SQLite persists across DB reopen ────────────────────────

func TestE2ETstateSQLiteSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tstate_e2e.db")

	// ── Open DB #1, insert a task, close. ──────────────────────────────────
	db1, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	st1, err := tstore.NewSQLite(db1)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	k1 := tstate.NewKernel(st1, tstate.KernelConfig{IDGen: tstate.SequentialIDs("p-")})

	ctx := context.Background()
	taskID, err := k1.Admit(ctx, spec.TaskSpec{
		Goal:      "persist across restart",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-sqlite-1",
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := k1.MarkReady(ctx, taskID); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	// Verify it's in ready state before closing.
	ts1, err := k1.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get before close: %v", err)
	}
	if ts1.Status != types.StatusReady {
		t.Fatalf("expected ready, got %s", ts1.Status)
	}
	_ = db1.Close()

	// ── Reopen DB #2, verify task is still present and in correct state. ───
	db2, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()
	st2, err := tstore.NewSQLite(db2)
	if err != nil {
		t.Fatalf("NewSQLite db2: %v", err)
	}
	k2 := tstate.NewKernel(st2, tstate.KernelConfig{IDGen: tstate.SequentialIDs("p-")})

	ts2, err := k2.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if ts2.Status != types.StatusReady {
		t.Fatalf("after reopen: expected ready, got %s", ts2.Status)
	}
	if ts2.LeafSpec.Goal != "persist across restart" {
		t.Fatalf("after reopen: expected goal %q, got %q", "persist across restart", ts2.LeafSpec.Goal)
	}
	if ts2.SessionID != "sess-sqlite-1" {
		t.Fatalf("after reopen: expected sessionID %q, got %q", "sess-sqlite-1", ts2.SessionID)
	}
}

// openSQLiteDB opens (or creates) a SQLite database at path with WAL mode.
func openSQLiteDB(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode=WAL&_pragma=busy_timeout=5000&_pragma=synchronous=NORMAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ─── Task 48b: msgbus SQLite persists queued messages across reopen ──────────

func TestE2EMsgbusSQLiteSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "msgbus_e2e.db")

	// ── Enqueue via Bus #1. ─────────────────────────────────────────────────
	db1, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	mst1, err := mstore.NewSQLite(db1)
	if err != nil {
		t.Fatalf("NewSQLite msgbus: %v", err)
	}
	bus1 := msgbus.NewInMem(mst1)
	ctx := context.Background()
	msg := msgbus.AgentMessage{
		MsgID:   "persist-test-msg-1",
		Kind:    msgbus.KindTask,
		To:      msgbus.AddrQueue("leaf"),
		TaskID:  "t-999",
		Payload: msgbus.TaskMessage{TaskID: "t-999", TaskType: "leaf", Task: "do it"},
	}
	if err := bus1.Publish(ctx, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_ = db1.Close()

	// ── Dequeue via Bus #2. ─────────────────────────────────────────────────
	db2, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()
	mst2, err := mstore.NewSQLite(db2)
	if err != nil {
		t.Fatalf("NewSQLite msgbus2: %v", err)
	}
	bus2 := msgbus.NewInMem(mst2)
	defer bus2.Close()

	deqCtx, deqCancel := context.WithTimeout(ctx, 2*time.Second)
	defer deqCancel()
	got, err := bus2.Dequeue(deqCtx, "leaf", "consumer-0")
	if err != nil {
		t.Fatalf("Dequeue after reopen: %v", err)
	}
	if got.MsgID != msg.MsgID {
		t.Fatalf("expected msg_id %q, got %q", msg.MsgID, got.MsgID)
	}
}

// ─── Task 49: lease expiry → FailOrRetry (if retryable budget) ───────────────

func TestE2ELeaseExpiredTriggersFailOrRetry(t *testing.T) {
	ctx := context.Background()

	tst := tstore.NewMemory()
	defer tst.Close()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{
		IDGen:           tstate.SequentialIDs("lr-"),
		DefaultLeaseTTL: 50 * time.Millisecond, // very short lease
	})

	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)
	defer bus.Close()

	// Admit + claim a task directly (bypassing scheduler).
	taskID, err := kernel.Admit(ctx, spec.TaskSpec{
		Goal:     "lease expiry test",
		Hint:     spec.Hint{Kind: types.KindReact},
		Budget:   types.Budget{MaxFailures: 3}, // retryable
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := kernel.MarkReady(ctx, taskID); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	// Claim with the short lease TTL.
	if err := kernel.Claim(ctx, taskID, "worker-1", 50*time.Millisecond, 0); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Verify running.
	ts, _ := kernel.Get(ctx, taskID)
	if ts.Status != types.StatusRunning {
		t.Fatalf("expected running, got %s", ts.Status)
	}

	// Wait for lease to expire.
	time.Sleep(100 * time.Millisecond)

	// Trigger FailOrRetry as the reaper would via onExpire.
	if err := kernel.Expire(ctx, taskID, types.FailLeaseExpired, 0); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	// With MaxFailures=3 and attempt=0, nextAttempt=1 < 3, so task goes back to ready.
	ts, err = kernel.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get after expire: %v", err)
	}
	// FailOrRetry retries when retryable AND nextAttempt < maxFailures.
	if ts.Status != types.StatusReady {
		t.Fatalf("expected ready (retry), got %s (last_error=%q)", ts.Status, ts.LastError)
	}
	if ts.Attempt != 1 {
		t.Fatalf("expected attempt=1, got %d", ts.Attempt)
	}
}

// ─── Task 49b: staging fallback — ConfirmSucceededFromStaging ────────────────

func TestE2EStagingFallbackConfirmsSuccess(t *testing.T) {
	ctx := context.Background()

	tst := tstore.NewMemory()
	defer tst.Close()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("sf-")})
	staging := tstate.NewStagingWriter(tst)

	// Admit and claim a task (simulating a running worker).
	taskID, err := kernel.Admit(ctx, spec.TaskSpec{
		Goal: "staging fallback test",
		Hint: spec.Hint{Kind: types.KindReact},
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := kernel.MarkReady(ctx, taskID); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := kernel.Claim(ctx, taskID, "worker-1", 30*time.Second, 0); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Worker stages the result (v3.1-R3: before publishing lifecycle{completed}).
	stagedRef := types.Ref("meta.json")
	if err := staging.StageResult(ctx, taskID, stagedRef, 0); err != nil {
		t.Fatalf("StageResult: %v", err)
	}

	// Verify StagedResultRef is persisted.
	ts, err := kernel.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ts.StagedResultRef != stagedRef {
		t.Fatalf("expected StagedResultRef=%q, got %q", stagedRef, ts.StagedResultRef)
	}

	// Simulate: lifecycle{completed} is lost (bus drop), but reaper detects
	// StagedResultRef != "" → calls ConfirmSucceededFromStaging.
	if err := kernel.ConfirmSucceededFromStaging(ctx, taskID, stagedRef, 0); err != nil {
		t.Fatalf("ConfirmSucceededFromStaging: %v", err)
	}

	// Task must now be succeeded with ResultRef populated.
	ts, err = kernel.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get after confirm: %v", err)
	}
	if ts.Status != types.StatusSucceeded {
		t.Fatalf("expected succeeded, got %s", ts.Status)
	}
	if ts.ResultRef != stagedRef {
		t.Fatalf("expected ResultRef=%q, got %q", stagedRef, ts.ResultRef)
	}
}

// TestE2EStagingFallbackRejectsStaleAttempt verifies the epoch guard in
// ConfirmSucceededFromStaging prevents double-spend after a retry.
func TestE2EStagingFallbackRejectsStaleAttempt(t *testing.T) {
	ctx := context.Background()

	tst := tstore.NewMemory()
	defer tst.Close()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("sg-")})
	staging := tstate.NewStagingWriter(tst)

	taskID, err := kernel.Admit(ctx, spec.TaskSpec{
		Goal:   "double-spend guard test",
		Hint:   spec.Hint{Kind: types.KindReact},
		Budget: types.Budget{MaxFailures: 3},
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := kernel.MarkReady(ctx, taskID); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := kernel.Claim(ctx, taskID, "worker-1", 30*time.Second, 0); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Stage result at attempt 0.
	stagedRef := types.Ref("meta.json")
	if err := staging.StageResult(ctx, taskID, stagedRef, 0); err != nil {
		t.Fatalf("StageResult: %v", err)
	}

	// Simulate a retry (attempt advances to 1 via FailOrRetry + re-claim).
	if err := kernel.Expire(ctx, taskID, types.FailLeaseExpired, 0); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	// Task is now ready with attempt=1; re-claim it.
	if err := kernel.Claim(ctx, taskID, "worker-2", 30*time.Second, 1); err != nil {
		t.Fatalf("Claim attempt 1: %v", err)
	}

	// Now stale reaper fires ConfirmSucceededFromStaging with old attempt=0.
	err = kernel.ConfirmSucceededFromStaging(ctx, taskID, stagedRef, 0 /* stale */)
	if err == nil {
		t.Fatal("expected ConfirmSucceededFromStaging to fail on stale attempt, but got nil")
	}

	// Task must still be running (new attempt).
	ts, _ := kernel.Get(ctx, taskID)
	if ts.Status != types.StatusRunning {
		t.Fatalf("expected running after stale confirm rejected, got %s", ts.Status)
	}
}
