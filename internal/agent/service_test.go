package agent

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// fakeStore is an in-memory AgentStore for service tests. Not concurrency-
// safe beyond the basics — that's fine for sequential test code.
type fakeStore struct {
	mu   sync.Mutex
	defs map[string]*AgentDefinition
}

func newFakeStore() *fakeStore {
	return &fakeStore{defs: make(map[string]*AgentDefinition)}
}

func (s *fakeStore) Create(_ context.Context, def *AgentDefinition) (*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.defs[def.Name]; exists {
		return nil, errAlreadyExists
	}
	cp := *def
	s.defs[def.Name] = &cp
	return &cp, nil
}

func (s *fakeStore) Get(_ context.Context, name string) (*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[name]
	if !ok {
		return nil, errNotFound
	}
	cp := *d
	return &cp, nil
}

func (s *fakeStore) List(_ context.Context, _ *AgentFilter) ([]*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*AgentDefinition, 0, len(s.defs))
	for _, d := range s.defs {
		cp := *d
		out = append(out, &cp)
	}
	return out, nil
}

func (s *fakeStore) Update(_ context.Context, name string, u *AgentUpdate) (*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[name]
	if !ok {
		return nil, errNotFound
	}
	if u.DisplayName != nil {
		d.DisplayName = *u.DisplayName
	}
	if u.Description != nil {
		d.Description = *u.Description
	}
	cp := *d
	return &cp, nil
}

func (s *fakeStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.defs, name)
	return nil
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const (
	errAlreadyExists = sentinelErr("already exists")
	errNotFound      = sentinelErr("not found")
)

// TestLoadAllToRegistry_NewNonBuiltinLoads exercises the non-builtin
// branch — user-imported YAML still loads as-is.
func TestLoadAllToRegistry_NewNonBuiltinLoads(t *testing.T) {
	store := newFakeStore()
	reg := NewAgentDefinitionRegistry()
	svc := NewAgentService(store, reg, zap.NewNop())

	custom := &AgentDefinition{
		Name:        "custom-imported",
		Description: "from yaml",
		AgentType:   "sync",
		Profile:     "worker",
		Source:      "yaml",
		IsBuiltin:   false,
	}
	if _, err := store.Create(context.Background(), custom); err != nil {
		t.Fatalf("create custom: %v", err)
	}

	if err := svc.LoadAllToRegistry(context.Background()); err != nil {
		t.Fatalf("LoadAllToRegistry: %v", err)
	}
	if reg.Get("custom-imported") == nil {
		t.Error("non-builtin definition should load through")
	}
}
