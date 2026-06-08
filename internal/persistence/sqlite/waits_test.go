package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"harnessclaw-go/internal/legacy/wait"
)

// newTestWaitStore opens a fresh DB and a WaitStore over it. Reuses
// newTestStore to ensure the session schema lives alongside the waits
// schema (which is the production layout).
func newTestWaitStore(t *testing.T) (*WaitStore, *Store) {
	t.Helper()
	sess := newTestStore(t)
	ws, err := NewWaitStore(sess.db)
	if err != nil {
		t.Fatalf("NewWaitStore: %v", err)
	}
	return ws, sess
}

func sampleWait(id string) wait.PendingWait {
	return wait.PendingWait{
		RequestID:     id,
		SessionID:     "sess_x",
		TraceID:       "tr_x",
		Kind:          wait.KindQuestion,
		CorrelationID: "toolu_y",
		PromptFrame:   []byte(`{"type":"prompt.user","payload":{"kind":"question"}}`),
		Anchor: wait.Anchor{
			MessageIndex:    7,
			AgentPath:       "main",
			CoordinatorMode: "react",
		},
		CreatedAt: time.Now().Truncate(time.Millisecond),
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Millisecond),
	}
}

func TestWaitStore_SaveGet(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()

	w := sampleWait("req_a1")
	if err := ws.Save(ctx, w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := ws.Get(ctx, "req_a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.SessionID != w.SessionID || got.CorrelationID != w.CorrelationID {
		t.Errorf("Get mismatch: %+v", got)
	}
	if got.Anchor.MessageIndex != 7 || got.Anchor.AgentPath != "main" {
		t.Errorf("Anchor lost in round-trip: %+v", got.Anchor)
	}
	if string(got.PromptFrame) != string(w.PromptFrame) {
		t.Errorf("PromptFrame lost: %s", got.PromptFrame)
	}
}

func TestWaitStore_GetNotFound(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	got, err := ws.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get on missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestWaitStore_Delete(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()

	if err := ws.Save(ctx, sampleWait("req_del")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := ws.Delete(ctx, "req_del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := ws.Get(ctx, "req_del")
	if got != nil {
		t.Errorf("Delete didn't remove: %+v", got)
	}
	// Idempotent: second delete is fine.
	if err := ws.Delete(ctx, "req_del"); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

func TestWaitStore_ListBySession(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		w := sampleWait("req_s_" + string(rune('a'+i)))
		w.SessionID = "sess_target"
		if err := ws.Save(ctx, w); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	// One wait for a different session.
	w := sampleWait("req_other")
	w.SessionID = "sess_other"
	_ = ws.Save(ctx, w)

	got, err := ws.ListBySession(ctx, "sess_target")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("ListBySession len = %d, want 3", len(got))
	}
	got2, _ := ws.ListBySession(ctx, "sess_nonexistent")
	if got2 == nil {
		t.Error("ListBySession should return empty slice not nil")
	}
}

func TestWaitStore_DeleteExpired(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()
	now := time.Now()

	// 1 expired, 1 not, 1 with no expiry.
	expired := sampleWait("req_exp")
	expired.ExpiresAt = now.Add(-time.Hour)
	if err := ws.Save(ctx, expired); err != nil {
		t.Fatal(err)
	}

	fresh := sampleWait("req_fresh")
	fresh.ExpiresAt = now.Add(time.Hour)
	if err := ws.Save(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	noExp := sampleWait("req_no_exp")
	noExp.ExpiresAt = time.Time{}
	if err := ws.Save(ctx, noExp); err != nil {
		t.Fatal(err)
	}

	n, err := ws.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteExpired returned %d, want 1", n)
	}
	if got, _ := ws.Get(ctx, "req_exp"); got != nil {
		t.Error("expired wait should be gone")
	}
	if got, _ := ws.Get(ctx, "req_fresh"); got == nil {
		t.Error("fresh wait should remain")
	}
	if got, _ := ws.Get(ctx, "req_no_exp"); got == nil {
		t.Error("no-expiry wait should remain")
	}
}

func TestWaitStore_SaveValidation(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		w    wait.PendingWait
	}{
		{"missing RequestID", wait.PendingWait{SessionID: "s", Kind: wait.KindQuestion}},
		{"missing SessionID", wait.PendingWait{RequestID: "r", Kind: wait.KindQuestion}},
		{"missing Kind", wait.PendingWait{RequestID: "r", SessionID: "s"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ws.Save(ctx, c.w); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestWaitStore_ConcurrentSaves(t *testing.T) {
	ws, _ := newTestWaitStore(t)
	ctx := context.Background()

	const N = 100
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w := sampleWait("req_conc_" + string(rune('a'+(n%26))) + string(rune('0'+(n/26))))
			if err := ws.Save(ctx, w); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Save error: %v", err)
	}
}
