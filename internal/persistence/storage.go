// Package storage defines the persistence interface for sessions.
//
// Note: The canonical Store interface is defined in internal/engine/session
// (session.Store) to avoid circular imports. Implementations in this package
// (memory.Store, sqlite.Store) satisfy that interface.
//
// This file provides a convenience re-export and the ListSessions extended
// interface for admin/query use cases.
package storage

import (
	"context"

	"harnessclaw-go/internal/engine/session"
)

// Storage extends session.Store with listing and lifecycle methods.
type Storage interface {
	session.Store

	// ListSessions returns sessions matching the filter.
	ListSessions(ctx context.Context, filter *session.SessionFilter) ([]*session.SessionSummary, error)

	// Close releases storage resources.
	Close() error
}
