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

// TestLoadAllToRegistry_PreservesBuiltinTier is the regression guard for
// the bug observed 2026-05-06 where SQLite-loaded definitions silently
// stripped the Tier field, breaking ListForPlanner / Plan-mode dispatch.
//
// Setup mirrors the production startup order:
//  1. SyncBuiltins → registry has full builtins, store has lossy copies
//  2. LoadAllToRegistry → must NOT replace the in-memory builtin with
//     the lossy store version
func TestLoadAllToRegistry_PreservesBuiltinTier(t *testing.T) {
	store := newFakeStore()
	reg := NewAgentDefinitionRegistry()
	svc := NewAgentService(store, reg, zap.NewNop())

	if err := svc.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins: %v", err)
	}

	// At this point the in-memory registry has full builtins. Verify
	// the freelancer worker has Tier=SubAgent.
	beforeReload := reg.Get("freelancer")
	if beforeReload == nil {
		t.Fatal("freelancer should be registered as builtin")
	}
	if beforeReload.Tier != TierSubAgent {
		t.Fatalf("freelancer.Tier = %q before reload, want %q", beforeReload.Tier, TierSubAgent)
	}
	if len(beforeReload.OutputSchema) == 0 {
		t.Fatal("freelancer.OutputSchema should be populated by RegisterBuiltins")
	}

	// Simulate the lossy round-trip: pretend the SQLite store strips
	// Tier and OutputSchema (the actual schema does this today).
	for name, d := range store.defs {
		d.Tier = ""
		d.OutputSchema = nil
		store.defs[name] = d
	}

	// Reload from store. After this the bug would have replaced the
	// in-memory freelancer with an empty-Tier copy.
	if err := svc.LoadAllToRegistry(context.Background()); err != nil {
		t.Fatalf("LoadAllToRegistry: %v", err)
	}

	afterReload := reg.Get("freelancer")
	if afterReload == nil {
		t.Fatal("freelancer disappeared after reload")
	}
	if afterReload.Tier != TierSubAgent {
		t.Errorf("freelancer.Tier lost after reload: got %q, want %q",
			afterReload.Tier, TierSubAgent)
	}
	if len(afterReload.OutputSchema) == 0 {
		t.Errorf("freelancer.OutputSchema lost after reload")
	}

	// And ListForPlanner should still surface the worker — the actual
	// failure mode of the production bug.
	listings := reg.ListForPlanner()
	if len(listings) == 0 {
		t.Fatal("ListForPlanner returned empty after reload — Plan mode would fail")
	}
	foundWriter := false
	for _, l := range listings {
		if l.Name == "freelancer" {
			foundWriter = true
			break
		}
	}
	if !foundWriter {
		t.Errorf("ListForPlanner missing freelancer; got %d listings: %v",
			len(listings), listingNames(listings))
	}
}

// TestLoadAllToRegistry_AppliesUserEdits confirms the merge still lets
// operators edit user-mutable fields (DisplayName) on builtins via the
// console API. Without this guard, the merge could become "ignore
// everything from store".
func TestLoadAllToRegistry_AppliesUserEdits(t *testing.T) {
	store := newFakeStore()
	reg := NewAgentDefinitionRegistry()
	svc := NewAgentService(store, reg, zap.NewNop())

	if err := svc.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins: %v", err)
	}

	// Operator changes the display name via the console API (simulated
	// by mutating the store entry directly). Tier still missing in
	// store as before.
	for name, d := range store.defs {
		if name == "freelancer" {
			d.DisplayName = "重命名后的外援"
			d.Tier = "" // simulate lossy schema
		}
	}

	if err := svc.LoadAllToRegistry(context.Background()); err != nil {
		t.Fatalf("LoadAllToRegistry: %v", err)
	}

	w := reg.Get("freelancer")
	if w.DisplayName != "重命名后的外援" {
		t.Errorf("user edit lost; DisplayName = %q", w.DisplayName)
	}
	if w.Tier != TierSubAgent {
		t.Errorf("Tier overwritten by lossy store; got %q", w.Tier)
	}
}

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

func listingNames(ls []PlannerListing) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}
