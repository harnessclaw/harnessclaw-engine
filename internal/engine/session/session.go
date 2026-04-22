// Package session manages conversation sessions and their message history.
package session

import (
	"sync"
	"time"

	"harnessclaw-go/pkg/types"
)

// State represents the lifecycle state of a session.
type State string

const (
	StateActive    State = "active"
	StateIdle      State = "idle"
	StateArchived  State = "archived"
	StateTerminated State = "terminated"
)

// Session holds the state for a single conversation.
type Session struct {
	mu sync.RWMutex

	ID        string          `json:"id"`
	State     State           `json:"state"`
	Messages  []types.Message `json:"messages"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	// Token tracking
	TotalInputTokens  int `json:"total_input_tokens"`
	TotalOutputTokens int `json:"total_output_tokens"`

	// Channel metadata
	ChannelName string         `json:"channel_name"`
	UserID      string         `json:"user_id"`
	Metadata    map[string]any `json:"metadata,omitempty"`

	// onChange is called after AddMessage/SetMessages to enable auto-persistence.
	// Set by Manager after session creation/restoration. Must NOT be called
	// under s.mu to avoid deadlock (SaveSession acquires RLock via GetMessages).
	onChange func()
}

// SetOnChange sets the auto-persistence callback. Called once by Manager.
func (s *Session) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// RLockFields acquires a read lock for direct field access by storage.
func (s *Session) RLockFields()   { s.mu.RLock() }

// RUnlockFields releases the read lock.
func (s *Session) RUnlockFields() { s.mu.RUnlock() }

// AddMessage appends a message and updates token counts.
func (s *Session) AddMessage(msg types.Message) {
	s.mu.Lock()
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
	s.TotalInputTokens += msg.Tokens
	cb := s.onChange
	s.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// SetMessages replaces the full message history (used after compaction).
func (s *Session) SetMessages(msgs []types.Message) {
	s.mu.Lock()
	s.Messages = msgs
	s.UpdatedAt = time.Now()
	cb := s.onChange
	s.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// MessageCount returns the number of messages in the session.
func (s *Session) MessageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// GetMessages returns a copy of all messages.
func (s *Session) GetMessages() []types.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]types.Message, len(s.Messages))
	copy(result, s.Messages)
	return result
}

// SessionSummary is a lightweight view for listing sessions.
type SessionSummary struct {
	ID          string    `json:"id"`
	State       State     `json:"state"`
	MessageCount int      `json:"message_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ChannelName string    `json:"channel_name"`
	UserID      string    `json:"user_id"`
}

// SessionFilter defines criteria for listing sessions.
type SessionFilter struct {
	State       *State  `json:"state,omitempty"`
	ChannelName *string `json:"channel_name,omitempty"`
	UserID      *string `json:"user_id,omitempty"`
	Limit       int     `json:"limit,omitempty"`
	Offset      int     `json:"offset,omitempty"`
}
