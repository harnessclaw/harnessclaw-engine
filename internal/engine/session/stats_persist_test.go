package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/sessionstats"
	"harnessclaw-go/pkg/types"
)

// trackingStatsStore counts SaveSessionStats calls.
type trackingStatsStore struct {
	saves int64

	mu       sync.Mutex
	lastSeen types.SessionStats
}

func (s *trackingStatsStore) SaveSession(context.Context, *Session) error { return nil }
func (s *trackingStatsStore) LoadSession(context.Context, string) (*Session, error) {
	return nil, nil
}
func (s *trackingStatsStore) DeleteSession(context.Context, string) error { return nil }
func (s *trackingStatsStore) SaveSessionStats(_ context.Context, _ string, st types.SessionStats) error {
	atomic.AddInt64(&s.saves, 1)
	s.mu.Lock()
	s.lastSeen = st
	s.mu.Unlock()
	return nil
}
func (s *trackingStatsStore) LoadSessionStats(_ context.Context, _ string) (types.SessionStats, error) {
	return types.SessionStats{}, nil
}
func (s *trackingStatsStore) Saves() int64 { return atomic.LoadInt64(&s.saves) }
func (s *trackingStatsStore) LastSeen() types.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

func TestStatsPersistWorker_DebouncesBurst(t *testing.T) {
	store := &trackingStatsStore{}
	tr := sessionstats.NewTracker("sess_abc")
	w := newStatsPersistWorker("sess_abc", tr, store, zap.NewNop())
	defer w.Stop()
	tr.BindNotify(w.NotifyChan())

	for i := 0; i < 20; i++ {
		tr.RecordToolCall()
	}

	time.Sleep(statsPersistDebounce + 200*time.Millisecond)

	if got := store.Saves(); got < 1 || got > 3 {
		t.Errorf("Saves() = %d, want 1..3", got)
	}
	if got := store.LastSeen().ToolCalls; got != 20 {
		t.Errorf("lastSeen.ToolCalls = %d, want 20", got)
	}
}

func TestStatsPersistWorker_FlushNow(t *testing.T) {
	store := &trackingStatsStore{}
	tr := sessionstats.NewTracker("sess_abc")
	w := newStatsPersistWorker("sess_abc", tr, store, zap.NewNop())
	defer w.Stop()
	tr.BindNotify(w.NotifyChan())

	tr.RecordToolCall()
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if store.Saves() < 1 {
		t.Errorf("Flush did not trigger a save")
	}
}

func TestStatsPersistWorker_StopFinalFlush(t *testing.T) {
	store := &trackingStatsStore{}
	tr := sessionstats.NewTracker("sess_abc")
	w := newStatsPersistWorker("sess_abc", tr, store, zap.NewNop())
	tr.BindNotify(w.NotifyChan())

	tr.RecordToolCall()
	w.Stop()
	if store.Saves() < 1 {
		t.Errorf("Stop did not perform final flush")
	}
}
