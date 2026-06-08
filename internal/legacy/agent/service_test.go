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

// Regression: a stale "scheduler" record in the store (e.g. a legacy
// user-facing 小时 builtin migrated from a pre-tier-system version)
// must NOT overwrite the in-code RegisterBuiltins L2 scheduler. The
// L2 scheduler carries the freelance tool in AllowedTools — losing it
// silently bricks the L1→L2→L3 dispatch chain.
func TestLoadAllToRegistry_PreservesReservedBuiltins(t *testing.T) {
	store := newFakeStore()
	reg := NewAgentDefinitionRegistry()
	reg.RegisterBuiltins() // populates "scheduler" (L2, coordinator, has freelance)

	// Simulate the stale store record: a "scheduler" with the wrong
	// tool palette and agent_type that USED to be the user-facing
	// scheduling assistant.
	stale := &AgentDefinition{
		Name:         "scheduler",
		DisplayName:  "小时",
		Description:  "stale",
		AgentType:    "sync",
		Profile:      "worker",
		AllowedTools: []string{"read", "write", "edit", "bash", "glob"},
		Source:       "builtin",
	}
	if _, err := store.Create(context.Background(), stale); err != nil {
		t.Fatalf("create stale: %v", err)
	}

	svc := NewAgentService(store, reg, zap.NewNop())
	if err := svc.LoadAllToRegistry(context.Background()); err != nil {
		t.Fatalf("LoadAllToRegistry: %v", err)
	}

	got := reg.Get("scheduler")
	if got == nil {
		t.Fatal("scheduler vanished after LoadAllToRegistry")
	}
	if got.DisplayName == "小时" {
		t.Error("stale store record overwrote builtin scheduler — L2 dispatch broken")
	}
	// Hard check: builtin must still expose freelance.
	hasFreelance := false
	for _, name := range got.AllowedTools {
		if name == "freelance" {
			hasFreelance = true
			break
		}
	}
	if !hasFreelance {
		t.Errorf("L2 scheduler lost freelance tool after LoadAllToRegistry; AllowedTools=%v", got.AllowedTools)
	}
}
