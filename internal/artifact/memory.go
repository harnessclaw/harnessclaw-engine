package artifact

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-process Store implementation. Useful for tests, for
// short-lived sub-agent traces that never need persistence, and as a fast
// path before the SQLite write commits.
type MemoryStore struct {
	cfg Config

	mu        sync.RWMutex
	artifacts map[string]*Artifact
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore(cfg Config) *MemoryStore {
	if cfg.DefaultTTL == 0 && cfg.PreviewBytes == 0 {
		cfg = DefaultConfig()
	}
	return &MemoryStore{
		cfg:       cfg,
		artifacts: make(map[string]*Artifact),
	}
}

// Save implements Store.Save.
func (m *MemoryStore) Save(_ context.Context, in *SaveInput) (*Artifact, error) {
	now := time.Now().UTC()

	var parent *Artifact
	if in.ParentArtifactID != "" {
		m.mu.RLock()
		p, ok := m.artifacts[in.ParentArtifactID]
		m.mu.RUnlock()
		if ok {
			parent = cloneArtifact(p)
		}
	}

	a := resolveSaveInput(in, m.cfg, parent, now)

	m.mu.Lock()
	m.artifacts[a.ID] = a
	m.mu.Unlock()

	// Return a copy so callers can mutate without racing the store's
	// own goroutines.
	return cloneArtifact(a), nil
}

// Get implements Store.Get.
func (m *MemoryStore) Get(_ context.Context, id string) (*Artifact, error) {
	m.mu.RLock()
	a, ok := m.artifacts[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if a.IsExpired(time.Now().UTC()) {
		return nil, ErrNotFound
	}
	return cloneArtifact(a), nil
}

// List implements Store.List. Filters by TraceID/SessionID/AgentID/Tag.
func (m *MemoryStore) List(_ context.Context, filter *ListFilter) ([]*Artifact, error) {
	now := time.Now().UTC()

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Artifact, 0, len(m.artifacts))
	for _, a := range m.artifacts {
		if a.IsExpired(now) {
			continue
		}
		if filter != nil {
			if filter.TraceID != "" && a.TraceID != filter.TraceID {
				continue
			}
			if filter.SessionID != "" && a.SessionID != filter.SessionID {
				continue
			}
			if filter.AgentID != "" && a.Producer.AgentID != filter.AgentID {
				continue
			}
			if filter.Tag != "" && !hasTag(a.Tags, filter.Tag) {
				continue
			}
		}
		out = append(out, cloneArtifact(a))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Delete implements Store.Delete.
func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.artifacts[id]; !ok {
		return ErrNotFound
	}
	delete(m.artifacts, id)
	return nil
}

// PurgeExpired implements Store.PurgeExpired. Called by the janitor.
func (m *MemoryStore) PurgeExpired(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	purged := 0
	for id, a := range m.artifacts {
		if a.IsExpired(now) {
			delete(m.artifacts, id)
			purged++
		}
	}
	return purged, nil
}

// Close implements Store.Close. The memory store releases the map so a
// repeated test can't accidentally observe stale state, even though the
// GC would catch it eventually.
func (m *MemoryStore) Close() error {
	m.mu.Lock()
	m.artifacts = make(map[string]*Artifact)
	m.mu.Unlock()
	return nil
}

// hasTag is a tiny helper kept private because tag matching is intentionally
// exact-match for now. Once we add tag prefixes / glob, lift it into store.go.
func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// cloneArtifact returns a deep-enough copy that the caller can mutate
// fields without affecting the stored record. Slices and the Schema
// RawMessage are duplicated; primitive fields are copied by value.
func cloneArtifact(a *Artifact) *Artifact {
	cp := *a
	if a.Schema != nil {
		cp.Schema = append([]byte(nil), a.Schema...)
	}
	if a.Tags != nil {
		cp.Tags = append([]string(nil), a.Tags...)
	}
	if a.Consumers != nil {
		cp.Consumers = append([]string(nil), a.Consumers...)
	}
	if a.Access.ReadableBy != nil {
		cp.Access.ReadableBy = append([]string(nil), a.Access.ReadableBy...)
	}
	if a.Access.WritableBy != nil {
		cp.Access.WritableBy = append([]string(nil), a.Access.WritableBy...)
	}
	return &cp
}
