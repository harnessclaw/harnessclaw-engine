package sessionstats

import "sync"

// Registry is the lookup table that maps session IDs to live trackers.
// Owned by the session.Manager (created once, injected into the
// StatsProvider decorator and the HTTP handler).
type Registry struct {
	mu       sync.Mutex
	trackers map[string]*Tracker
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{trackers: make(map[string]*Tracker)}
}

// GetOrCreate returns the tracker for sessionID, creating one on first
// access. Safe for concurrent use.
func (r *Registry) GetOrCreate(sessionID string) *Tracker {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.trackers[sessionID]
	if !ok {
		t = NewTracker(sessionID)
		r.trackers[sessionID] = t
	}
	return t
}

// Get returns the tracker for sessionID without creating one. Returns
// nil when the session is unknown — callers should fall through to a
// persisted-snapshot lookup.
func (r *Registry) Get(sessionID string) *Tracker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trackers[sessionID]
}

// Drop removes the tracker for sessionID. Called from session idle
// cleanup after a final flush.
func (r *Registry) Drop(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.trackers, sessionID)
}
