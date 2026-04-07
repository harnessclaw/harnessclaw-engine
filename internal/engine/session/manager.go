package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"harnessclaw-go/pkg/types"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Store defines the persistence interface that the session manager depends on.
// This interface is defined here (not in the storage package) to avoid circular imports.
// Implementations in internal/storage/* satisfy this interface.
type Store interface {
	SaveSession(ctx context.Context, s *Session) error
	LoadSession(ctx context.Context, id string) (*Session, error)
	DeleteSession(ctx context.Context, id string) error
}

// Manager handles session lifecycle: creation, retrieval, persistence, and cleanup.
type Manager struct {
	mu      sync.RWMutex
	active  map[string]*Session // in-memory active sessions
	store   Store
	logger  *zap.Logger
	maxIdle time.Duration
}

// NewManager creates a session manager.
func NewManager(store Store, logger *zap.Logger, maxIdle time.Duration) *Manager {
	return &Manager{
		active:  make(map[string]*Session),
		store:   store,
		logger:  logger,
		maxIdle: maxIdle,
	}
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
		return s, nil
	}

	// Try loading from persistent storage
	stored, err := m.store.LoadSession(ctx, sessionID)
	if err == nil && stored != nil {
		stored.State = StateActive
		stored.UpdatedAt = time.Now()
		m.active[sessionID] = stored
		m.logger.Info("session restored from storage", zap.String("session_id", sessionID))
		return stored, nil
	}

	// Create new session
	s := &Session{
		ID:          sessionID,
		State:       StateActive,
		Messages:    make([]types.Message, 0),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ChannelName: channelName,
		UserID:      userID,
		Metadata:    make(map[string]any),
	}
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	m.active[s.ID] = s
	m.logger.Info("session created", zap.String("session_id", s.ID))
	return s, nil
}

// Get retrieves an active session. Returns nil if not found.
func (m *Manager) Get(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[sessionID]
}

// PersistAll saves all active sessions to storage.
func (m *Manager) PersistAll(ctx context.Context) error {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.active))
	for _, s := range m.active {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	var firstErr error
	for _, s := range sessions {
		if err := m.store.SaveSession(ctx, s); err != nil {
			m.logger.Error("failed to persist session", zap.String("session_id", s.ID), zap.Error(err))
			if firstErr == nil {
				firstErr = fmt.Errorf("persist session %s: %w", s.ID, err)
			}
		}
	}
	return firstErr
}

// CleanupIdle archives sessions that have been idle longer than maxIdle.
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

			if err := m.store.SaveSession(ctx, s); err != nil {
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
