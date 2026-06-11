package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/pkg/types"
)

// Store defines the persistence interface that the session manager depends on.
// This interface is defined here (not in the storage package) to avoid circular imports.
// Implementations in internal/storage/* satisfy this interface.
type Store interface {
	SaveSession(ctx context.Context, s *Session) error
	LoadSession(ctx context.Context, id string) (*Session, error)
	DeleteSession(ctx context.Context, id string) error

	// SaveSessionStats persists the metrics snapshot to the session's
	// backing storage. Implementations should treat missing-session-row
	// as an error so the caller can re-issue SaveSession first.
	SaveSessionStats(ctx context.Context, sessionID string, stats types.SessionStats) error

	// LoadSessionStats returns the persisted snapshot. When the session
	// row exists but no metrics have been written, returns a zero
	// SessionStats and a nil error. When the session row does not exist
	// returns a zero SessionStats and a nil error (handlers map this to
	// 404).
	LoadSessionStats(ctx context.Context, sessionID string) (types.SessionStats, error)
}

// Manager handles session lifecycle: creation, retrieval, persistence, and cleanup.
type Manager struct {
	mu            sync.RWMutex
	active        map[string]*Session            // in-memory active sessions
	workers       map[string]*persistWorker      // sessionID → debounced persist worker
	statsWorkers  map[string]*statsPersistWorker // sessionID → stats persist worker
	statsRegistry *sessionstats.Registry
	store         Store
	logger        *zap.Logger
	maxIdle       time.Duration
}

// NewManager creates a session manager.
func NewManager(store Store, logger *zap.Logger, maxIdle time.Duration) *Manager {
	return &Manager{
		active:       make(map[string]*Session),
		workers:      make(map[string]*persistWorker),
		statsWorkers: make(map[string]*statsPersistWorker),
		store:        store,
		logger:       logger,
		maxIdle:      maxIdle,
	}
}

// BindStatsRegistry injects the shared Registry that the StatsProvider
// decorator and the HTTP handler also read. Call once at server startup
// before the first GetOrCreate. Passing nil disables stats tracking.
func (m *Manager) BindStatsRegistry(reg *sessionstats.Registry) {
	m.mu.Lock()
	m.statsRegistry = reg
	m.mu.Unlock()
}

// GetOrCreate retrieves an active session or creates a new one.
func (m *Manager) GetOrCreate(ctx context.Context, sessionID string, channelName string, userID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check active sessions first
	if s, ok := m.active[sessionID]; ok {
		s.mu.Lock()
		s.State = StateActive
		s.UpdatedAt = time.Now()
		s.mu.Unlock()
		m.bindOnChange(s)
		m.bindStatsLocked(ctx, s)
		return s, nil
	}

	// Try loading from persistent storage
	stored, err := m.store.LoadSession(ctx, sessionID)
	if err == nil && stored != nil {
		stored.State = StateActive
		stored.UpdatedAt = time.Now()
		stored.ensureRuntime()
		m.active[sessionID] = stored
		m.bindOnChange(stored)
		m.bindStatsLocked(ctx, stored)
		m.logger.Info("session restored from storage", zap.String("session_id", sessionID))
		return stored, nil
	}

	// Create new session
	s := &Session{
		ID:           sessionID,
		State:        StateActive,
		Messages:     make([]types.Message, 0),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		ChannelName:  channelName,
		UserID:       userID,
		Metadata:     make(map[string]any),
		Awaits:       NewAwaits(),
		allowedTools: make(map[string]bool),
	}
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	m.active[s.ID] = s
	// Eagerly persist the new session row so per-session workers (stats,
	// future ones) that fire UPDATE-based writes don't trip on a missing
	// row before the first message lands. Best-effort: a transient
	// failure here will be retried by the persist worker on the next
	// mutation.
	if err := m.store.SaveSession(ctx, s); err != nil {
		m.logger.Warn("eager save new session",
			zap.String("session_id", s.ID), zap.Error(err))
	}
	m.bindOnChange(s)
	m.bindStatsLocked(ctx, s)
	m.logger.Info("session created", zap.String("session_id", s.ID))
	return s, nil
}

// Get retrieves an active session. Returns nil if not found.
func (m *Manager) Get(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[sessionID]
}

// PersistSession synchronously flushes a single session through its
// debounced worker (if one exists) or falls back to a direct store
// write. Synchronous — caller blocks until the write completes.
//
// Hot-path note: routine session mutations (AddMessage, SetMessages)
// SHOULD NOT call PersistSession directly. They go through the
// session's onChange callback, which fires a non-blocking notify on
// the persist worker. Only explicit "I need this on disk now" callers
// (shutdown, snapshot APIs) should call PersistSession.
func (m *Manager) PersistSession(ctx context.Context, s *Session) {
	m.mu.RLock()
	w := m.workers[s.ID]
	m.mu.RUnlock()
	if w != nil {
		if err := w.Flush(ctx); err != nil {
			m.logger.Error("failed to flush session via worker",
				zap.String("session_id", s.ID), zap.Error(err))
		}
		return
	}
	// No worker (e.g. session not registered). Direct write fallback.
	if err := m.store.SaveSession(ctx, s); err != nil {
		m.logger.Error("failed to persist session",
			zap.String("session_id", s.ID), zap.Error(err))
	}
}

// bindOnChange wires the session's onChange callback to a per-session
// debounced persist worker. The worker is started on first call; the
// callback is set once and is a non-blocking notify (no goroutine per
// mutation, no race for the SQLite write lock).
//
// Caller MUST hold m.mu (write lock) — accesses m.workers.
func (m *Manager) bindOnChange(s *Session) {
	if _, ok := m.workers[s.ID]; ok {
		return // already bound
	}
	w := newPersistWorker(s, m.store, m.logger)
	m.workers[s.ID] = w
	s.SetOnChange(w.notify)
}

// bindStatsLocked attaches (or reattaches) the stats tracker + persist
// worker for s. Caller MUST hold m.mu (write lock). No-op when no
// Registry is bound (tests / channels that don't enable metrics).
func (m *Manager) bindStatsLocked(ctx context.Context, s *Session) {
	if m.statsRegistry == nil {
		return
	}
	if _, ok := m.statsWorkers[s.ID]; ok {
		return
	}
	tr := m.statsRegistry.GetOrCreate(s.ID)
	if snap, err := m.store.LoadSessionStats(ctx, s.ID); err == nil && snap.SessionID != "" {
		tr.RestoreFrom(snap)
	} else if err != nil {
		m.logger.Warn("load session stats during bind",
			zap.String("session_id", s.ID), zap.Error(err))
	}
	w := newStatsPersistWorker(s.ID, tr, m.store, m.logger)
	m.statsWorkers[s.ID] = w
	tr.BindNotify(w.NotifyChan())
}

// FlushStats triggers an immediate, blocking flush of the stats
// persist worker for sessionID. Safe to call from arbitrary
// goroutines; no-op when stats are not wired or the session is
// unknown.
func (m *Manager) FlushStats(ctx context.Context, sessionID string) {
	m.mu.RLock()
	w := m.statsWorkers[sessionID]
	m.mu.RUnlock()
	if w == nil {
		return
	}
	if err := w.Flush(ctx); err != nil {
		m.logger.Warn("flush session stats", zap.String("session_id", sessionID), zap.Error(err))
	}
}

// PersistAll synchronously flushes all active sessions to storage.
// Used at shutdown and on explicit snapshot calls. Each session is
// flushed through its worker (debounced flush honours flushNow path).
func (m *Manager) PersistAll(ctx context.Context) error {
	m.mu.RLock()
	workers := make([]*persistWorker, 0, len(m.workers))
	sessIDs := make([]string, 0, len(m.workers))
	for id, w := range m.workers {
		workers = append(workers, w)
		sessIDs = append(sessIDs, id)
	}
	m.mu.RUnlock()

	var firstErr error
	for i, w := range workers {
		if err := w.Flush(ctx); err != nil {
			m.logger.Error("failed to persist session", zap.String("session_id", sessIDs[i]), zap.Error(err))
			if firstErr == nil {
				firstErr = fmt.Errorf("persist session %s: %w", sessIDs[i], err)
			}
		}
	}
	return firstErr
}

// Shutdown stops all persist workers, performing a final flush per
// session. Call once on server shutdown. Safe to call multiple times.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	workers := m.workers
	statsWorkers := m.statsWorkers
	m.workers = make(map[string]*persistWorker)
	m.statsWorkers = make(map[string]*statsPersistWorker)
	m.mu.Unlock()
	for _, w := range workers {
		w.Stop()
	}
	for _, w := range statsWorkers {
		w.Stop()
	}
}

// CleanupIdle archives sessions that have been idle longer than maxIdle.
// Archived sessions get a final flush through their worker, then the
// worker is stopped and removed.
func (m *Manager) CleanupIdle(ctx context.Context) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	threshold := time.Now().Add(-m.maxIdle)
	archived := 0

	for id, s := range m.active {
		s.mu.RLock()
		updatedAt := s.UpdatedAt
		s.mu.RUnlock()

		if updatedAt.Before(threshold) {
			s.mu.Lock()
			s.State = StateArchived
			s.mu.Unlock()

			if w, ok := m.workers[id]; ok {
				if err := w.Flush(ctx); err != nil {
					m.logger.Error("failed to flush before archive", zap.String("session_id", id), zap.Error(err))
					continue
				}
				w.Stop()
				delete(m.workers, id)
				if sw, ok := m.statsWorkers[id]; ok {
					if err := sw.Flush(ctx); err != nil {
						m.logger.Error("flush stats before archive", zap.String("session_id", id), zap.Error(err))
					}
					sw.Stop()
					delete(m.statsWorkers, id)
				}
				if m.statsRegistry != nil {
					m.statsRegistry.Drop(id)
				}
			} else if err := m.store.SaveSession(ctx, s); err != nil {
				m.logger.Error("failed to archive session", zap.String("session_id", id), zap.Error(err))
				continue
			}
			delete(m.active, id)
			archived++
			m.logger.Info("session archived", zap.String("session_id", id))
		}
	}
	return archived
}
