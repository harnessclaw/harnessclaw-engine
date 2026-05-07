package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

// countingStore implements Store and records every SaveSession call.
type countingStore struct {
	mu    sync.Mutex
	saves int64
	fail  bool // when true, every SaveSession returns an error
}

func (s *countingStore) SaveSession(_ context.Context, _ *Session) error {
	atomic.AddInt64(&s.saves, 1)
	if s.fail {
		return errSaveFailure
	}
	return nil
}
func (s *countingStore) LoadSession(_ context.Context, _ string) (*Session, error) {
	return nil, nil
}
func (s *countingStore) DeleteSession(_ context.Context, _ string) error { return nil }

func (s *countingStore) Saves() int64 { return atomic.LoadInt64(&s.saves) }

var errSaveFailure = simpleErr("simulated save failure")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func makeManager(t *testing.T, store Store) *Manager {
	t.Helper()
	m := NewManager(store, zap.NewNop(), time.Hour)
	t.Cleanup(m.Shutdown)
	return m
}

// TestPersistWorker_DebouncesBurst verifies that 100 mutations within
// the debounce window collapse into a single SaveSession call. This
// is the central guarantee that prevents the SQLITE_BUSY storm seen in
// production.
func TestPersistWorker_DebouncesBurst(t *testing.T) {
	store := &countingStore{}
	m := makeManager(t, store)

	s, err := m.GetOrCreate(context.Background(), "sess_burst", "ws", "u1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// 100 rapid mutations — old code fired 100 goroutines + 100 writes.
	for i := 0; i < 100; i++ {
		s.AddMessage(types.Message{ID: "m", Role: types.RoleUser, Tokens: 1})
	}

	// Wait long enough for the debounce window to elapse + write to land.
	time.Sleep(persistDebounce + 200*time.Millisecond)

	got := store.Saves()
	if got > 3 {
		t.Errorf("Saves = %d after burst; want ≤3 (debounce should coalesce)", got)
	}
	if got == 0 {
		t.Errorf("Saves = 0; debounced flush never fired")
	}
}

// TestPersistWorker_NoFloodAtAddMessage verifies that AddMessage NEVER
// blocks the caller even if the store is wedged (slow). This is the
// hot-path requirement: streaming chunks must not slow down for I/O.
func TestPersistWorker_NoFloodAtAddMessage(t *testing.T) {
	slowStore := &slowSaveStore{delay: 200 * time.Millisecond}
	m := makeManager(t, slowStore)

	s, _ := m.GetOrCreate(context.Background(), "sess_fast", "ws", "")

	start := time.Now()
	for i := 0; i < 50; i++ {
		s.AddMessage(types.Message{ID: "m", Tokens: 1})
	}
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("50 AddMessage calls took %v; expected <50ms (caller should never block on I/O)", elapsed)
	}
}

type slowSaveStore struct {
	delay time.Duration
}

func (s *slowSaveStore) SaveSession(_ context.Context, _ *Session) error {
	time.Sleep(s.delay)
	return nil
}
func (s *slowSaveStore) LoadSession(_ context.Context, _ string) (*Session, error) {
	return nil, nil
}
func (s *slowSaveStore) DeleteSession(_ context.Context, _ string) error { return nil }

// TestPersistWorker_FlushSync verifies that PersistSession blocks until
// the in-flight write completes — required by shutdown / snapshot
// callers that need on-disk state to be current.
func TestPersistWorker_FlushSync(t *testing.T) {
	store := &countingStore{}
	m := makeManager(t, store)

	s, _ := m.GetOrCreate(context.Background(), "sess_flush", "ws", "")
	s.AddMessage(types.Message{ID: "m1"})

	before := store.Saves()
	m.PersistSession(context.Background(), s)
	after := store.Saves()
	if after <= before {
		t.Errorf("PersistSession should have flushed; saves before=%d after=%d", before, after)
	}
}

// TestPersistWorker_PersistAllFlushes verifies PersistAll triggers a
// flush per session, not just the debounce timer.
func TestPersistWorker_PersistAllFlushes(t *testing.T) {
	store := &countingStore{}
	m := makeManager(t, store)

	s1, _ := m.GetOrCreate(context.Background(), "s1", "ws", "")
	s2, _ := m.GetOrCreate(context.Background(), "s2", "ws", "")
	s1.AddMessage(types.Message{ID: "x"})
	s2.AddMessage(types.Message{ID: "y"})

	before := store.Saves()
	if err := m.PersistAll(context.Background()); err != nil {
		t.Fatalf("PersistAll: %v", err)
	}
	after := store.Saves()
	// 2 flushes (one per session) — each may also include the dirty
	// debounce that was about to fire, but at minimum we expect 2.
	if after-before < 2 {
		t.Errorf("PersistAll saves = %d; want ≥2", after-before)
	}
}

// TestPersistWorker_ShutdownFlushes verifies that Shutdown performs a
// final flush per worker so on-disk state is current at server exit.
func TestPersistWorker_ShutdownFlushes(t *testing.T) {
	store := &countingStore{}
	m := NewManager(store, zap.NewNop(), time.Hour)

	s, _ := m.GetOrCreate(context.Background(), "sess_shut", "ws", "")
	s.AddMessage(types.Message{ID: "m"})

	before := store.Saves()
	m.Shutdown()
	after := store.Saves()

	if after <= before {
		t.Errorf("Shutdown should flush dirty workers; before=%d after=%d", before, after)
	}
}

// TestPersistWorker_ConcurrentSessionsIndependentDebounce verifies
// per-session worker isolation: each session's debounce window doesn't
// stomp on the others.
func TestPersistWorker_ConcurrentSessionsIndependentDebounce(t *testing.T) {
	store := &countingStore{}
	m := makeManager(t, store)

	const N = 10
	sessions := make([]*Session, N)
	for i := 0; i < N; i++ {
		s, _ := m.GetOrCreate(context.Background(), id(i), "ws", "")
		sessions[i] = s
	}

	// Each session takes a burst of mutations.
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.AddMessage(types.Message{ID: "m", Tokens: 1})
			}
		}(s)
	}
	wg.Wait()
	time.Sleep(persistDebounce + 200*time.Millisecond)

	got := store.Saves()
	// Each of N sessions should have flushed at least once, but far
	// less than N*20 (the un-debounced count).
	if got < N {
		t.Errorf("Saves = %d; expected ≥%d (each session at least once)", got, N)
	}
	if got > N*5 {
		t.Errorf("Saves = %d; expected ≤%d (debounce too lax)", got, N*5)
	}
}

func id(i int) string {
	return "sess_" + string(rune('a'+i))
}
