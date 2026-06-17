package agentmgmt

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/engine/agent/definition"
)

// fakeStore is an in-memory AgentStore for service tests. Not concurrency-
// safe beyond the basics — that's fine for sequential test code.
type fakeStore struct {
	mu   sync.Mutex
	defs map[string]*definition.AgentDefinition
}

func newFakeStore() *fakeStore {
	return &fakeStore{defs: make(map[string]*definition.AgentDefinition)}
}

func (s *fakeStore) Create(_ context.Context, def *definition.AgentDefinition) (*definition.AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.defs[def.Name]; exists {
		return nil, errAlreadyExists
	}
	cp := *def
	s.defs[def.Name] = &cp
	return &cp, nil
}

func (s *fakeStore) Get(_ context.Context, name string) (*definition.AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[name]
	if !ok {
		return nil, errNotFound
	}
	cp := *d
	return &cp, nil
}

func (s *fakeStore) List(_ context.Context, _ *AgentFilter) ([]*definition.AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*definition.AgentDefinition, 0, len(s.defs))
	for _, d := range s.defs {
		cp := *d
		out = append(out, &cp)
	}
	return out, nil
}

func (s *fakeStore) Update(_ context.Context, name string, u *AgentUpdate) (*definition.AgentDefinition, error) {
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
	reg := definition.NewRegistry()
	svc := NewAgentService(store, reg, zap.NewNop())

	custom := &definition.AgentDefinition{
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

// Regression: a stale "freelancer" record in the store must NOT overwrite
// the in-code builtin.RegisterAll freelancer definition. Builtin freelancer
// carries skill-driven tool palette + correct profile — losing it bricks L3.
// (该测试原本守的是 "scheduler"，L2 删除后改用 freelancer 守同一不变量。)
func TestLoadAllToRegistry_PreservesReservedBuiltins(t *testing.T) {
	store := newFakeStore()
	reg := definition.NewRegistry()
	if err := builtin.RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	stale := &definition.AgentDefinition{
		Name:         "freelancer",
		DisplayName:  "stale-display",
		Description:  "stale",
		AgentType:    "sync",
		Profile:      "worker",
		AllowedTools: []string{"read"},
		Source:       "builtin",
	}
	if _, err := store.Create(context.Background(), stale); err != nil {
		t.Fatalf("create stale: %v", err)
	}

	svc := NewAgentService(store, reg, zap.NewNop())
	if err := svc.LoadAllToRegistry(context.Background()); err != nil {
		t.Fatalf("LoadAllToRegistry: %v", err)
	}

	got := reg.Get("freelancer")
	if got == nil {
		t.Fatal("freelancer vanished after LoadAllToRegistry")
	}
	if got.DisplayName == "stale-display" {
		t.Error("stale store record overwrote builtin freelancer")
	}
}
