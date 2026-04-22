// Package memory provides an in-memory storage implementation for testing.
// Production code always uses SQLite for persistence; this package exists
// solely to provide a lightweight Store for unit tests.
package memory

import (
	"context"
	"sync"

	"harnessclaw-go/internal/engine/session"
)

// Store is an in-memory session store.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
}

// New creates a memory store.
func New() *Store {
	return &Store{
		sessions: make(map[string]*session.Session),
	}
}

func (s *Store) SaveSession(_ context.Context, sess *session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *Store) LoadSession(_ context.Context, id string) (*session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, nil
	}
	return sess, nil
}

func (s *Store) ListSessions(_ context.Context, filter *session.SessionFilter) ([]*session.SessionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*session.SessionSummary, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if filter != nil && filter.State != nil && sess.State != *filter.State {
			continue
		}
		if filter != nil && filter.ChannelName != nil && sess.ChannelName != *filter.ChannelName {
			continue
		}
		if filter != nil && filter.UserID != nil && sess.UserID != *filter.UserID {
			continue
		}
		result = append(result, &session.SessionSummary{
			ID:           sess.ID,
			State:        sess.State,
			MessageCount: sess.MessageCount(),
			CreatedAt:    sess.CreatedAt,
			UpdatedAt:    sess.UpdatedAt,
			ChannelName:  sess.ChannelName,
			UserID:       sess.UserID,
		})
	}

	if filter != nil && filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (s *Store) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *Store) Close() error {
	return nil
}
