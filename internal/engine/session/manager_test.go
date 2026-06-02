package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/pkg/types"
)

type statsStub struct {
	mu    sync.Mutex
	saved types.SessionStats
}

func (s *statsStub) SaveSession(context.Context, *Session) error           { return nil }
func (s *statsStub) LoadSession(context.Context, string) (*Session, error) { return nil, nil }
func (s *statsStub) DeleteSession(context.Context, string) error           { return nil }
func (s *statsStub) SaveSessionStats(_ context.Context, _ string, st types.SessionStats) error {
	s.mu.Lock()
	s.saved = st
	s.mu.Unlock()
	return nil
}
func (s *statsStub) LoadSessionStats(_ context.Context, _ string) (types.SessionStats, error) {
	return types.SessionStats{}, nil
}
func (s *statsStub) Saved() types.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

func TestManager_CreatesStatsTrackerAndWorker(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &statsStub{}
	m := NewManager(store, zap.NewNop(), time.Hour)
	m.BindStatsRegistry(reg)
	defer m.Shutdown()

	ctx := context.Background()
	sess, err := m.GetOrCreate(ctx, "sess_abc", "ws", "user_1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if sess.ID != "sess_abc" {
		t.Errorf("sess.ID = %q", sess.ID)
	}
	tr := reg.Get("sess_abc")
	if tr == nil {
		t.Fatalf("Manager did not create a Tracker for the new session")
	}
	tr.RecordToolCall()
	time.Sleep(statsPersistDebounce + 200*time.Millisecond)
	if got := store.Saved().ToolCalls; got != 1 {
		t.Errorf("expected store.saved.ToolCalls=1, got %d", got)
	}
}

func TestManager_ReloadHydratesTracker(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &reloadStub{
		loaded: &Session{ID: "sess_old", State: StateActive, CreatedAt: time.Now(), UpdatedAt: time.Now(), Awaits: NewAwaits(), allowedTools: make(map[string]bool)},
		stats:  types.SessionStats{SessionID: "sess_old", InputTokens: 999, LLMCalls: 5},
	}
	m := NewManager(store, zap.NewNop(), time.Hour)
	m.BindStatsRegistry(reg)
	defer m.Shutdown()

	_, err := m.GetOrCreate(context.Background(), "sess_old", "ws", "user_1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	tr := reg.Get("sess_old")
	if tr == nil {
		t.Fatalf("no tracker")
	}
	if got := tr.Snapshot().InputTokens; got != 999 {
		t.Errorf("InputTokens = %d, want 999 (reload not hydrated)", got)
	}
}

type reloadStub struct {
	loaded *Session
	stats  types.SessionStats
}

func (s *reloadStub) SaveSession(context.Context, *Session) error { return nil }
func (s *reloadStub) LoadSession(_ context.Context, _ string) (*Session, error) {
	return s.loaded, nil
}
func (s *reloadStub) DeleteSession(context.Context, string) error                        { return nil }
func (s *reloadStub) SaveSessionStats(context.Context, string, types.SessionStats) error { return nil }
func (s *reloadStub) LoadSessionStats(_ context.Context, _ string) (types.SessionStats, error) {
	return s.stats, nil
}
