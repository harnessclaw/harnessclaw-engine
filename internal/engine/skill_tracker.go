package engine

import (
	"fmt"
	"sync"

	"harnessclaw-go/internal/skill"
)

// LoadedState distinguishes active skills (counted toward budget) from
// unloaded ones (still in messages history, but L3 instructed to ignore).
type LoadedState string

const (
	StateActive   LoadedState = "active"
	StateUnloaded LoadedState = "unloaded"
)

// LoadedSource records how a skill got into the tracker — candidate (L2
// preload at SpawnSync) vs runtime (LoadSkill in the loop).
type LoadedSource string

const (
	SourceCandidate LoadedSource = "candidate"
	SourceRuntime   LoadedSource = "runtime"
)

// LoadedEntry is the tracker's per-skill state.
type LoadedEntry struct {
	Skill  *skill.SkillFull
	State  LoadedState
	Source LoadedSource
}

// LoadedRef is the public-facing view returned by List() / events.
type LoadedRef struct {
	Name    string       `json:"name"`
	Version string       `json:"version,omitempty"`
	Source  LoadedSource `json:"source"`
}

// SkillTracker is a per-spawn state container shared by SpawnSync and the
// four skill self-management tools (LoadSkill / UnloadSkill / ListLoaded).
// It tracks WHICH skills are active vs unloaded and enforces a hard budget.
//
// What it does NOT do: modify tool pool, modify systemPrompt, modify
// messages history. Those are handled elsewhere.
type SkillTracker struct {
	mu        sync.Mutex
	entries   map[string]*LoadedEntry
	maxBudget int
}

// NewSkillTracker constructs a tracker with the given active-skill budget.
// Budget includes both L2 candidates and runtime LoadSkill.
func NewSkillTracker(maxBudget int) *SkillTracker {
	return &SkillTracker{
		entries:   make(map[string]*LoadedEntry),
		maxBudget: maxBudget,
	}
}

// Preload registers L2-provided candidate skills. All entries are active
// after Preload. Fails if len(skills) > maxBudget.
func (t *SkillTracker) Preload(skills []*skill.SkillFull) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(skills) > t.maxBudget {
		return fmt.Errorf("candidate skills %d > budget %d", len(skills), t.maxBudget)
	}
	for _, s := range skills {
		t.entries[s.Name] = &LoadedEntry{
			Skill:  s,
			State:  StateActive,
			Source: SourceCandidate,
		}
	}
	return nil
}

// Add registers a runtime-loaded skill. If the name is already active,
// returns nil (idempotent). Refuses when active count is at budget.
func (t *SkillTracker) Add(s *skill.SkillFull) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[s.Name]; ok && e.State == StateActive {
		return nil
	}
	if t.activeCountLocked() >= t.maxBudget {
		return fmt.Errorf("skill budget full (%d/%d): cannot add %q",
			t.activeCountLocked(), t.maxBudget, s.Name)
	}
	t.entries[s.Name] = &LoadedEntry{
		Skill:  s,
		State:  StateActive,
		Source: SourceRuntime,
	}
	return nil
}

// Reactivate switches an unloaded skill back to active. Refuses if budget
// is full or skill was never tracked.
func (t *SkillTracker) Reactivate(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[name]
	if !ok {
		return fmt.Errorf("skill %q never tracked", name)
	}
	if e.State == StateActive {
		return nil
	}
	if t.activeCountLocked() >= t.maxBudget {
		return fmt.Errorf("skill budget full (%d/%d): cannot reactivate %q",
			t.activeCountLocked(), t.maxBudget, name)
	}
	e.State = StateActive
	return nil
}

// MarkUnloaded flips an active skill to unloaded state, freeing budget.
// Returns error when the skill isn't tracked at all.
func (t *SkillTracker) MarkUnloaded(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[name]
	if !ok {
		return fmt.Errorf("skill %q not loaded", name)
	}
	if e.State == StateUnloaded {
		return fmt.Errorf("skill %q already unloaded", name)
	}
	e.State = StateUnloaded
	return nil
}

// Count returns the active count (the budget-relevant number).
func (t *SkillTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeCountLocked()
}

// Max returns the budget ceiling.
func (t *SkillTracker) Max() int { return t.maxBudget }

// List returns active and unloaded entries (in two slices).
func (t *SkillTracker) List() (active, unloaded []LoadedRef) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.entries {
		ref := LoadedRef{Name: e.Skill.Name, Version: e.Skill.Version, Source: e.Source}
		if e.State == StateActive {
			active = append(active, ref)
		} else {
			unloaded = append(unloaded, ref)
		}
	}
	return active, unloaded
}

// GetFull returns the full SkillFull (with body) for a tracked skill,
// or false if not tracked. State is ignored — both active and unloaded
// are returnable.
func (t *SkillTracker) GetFull(name string) (*skill.SkillFull, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[name]
	if !ok {
		return nil, false
	}
	return e.Skill, true
}

// IsActive returns whether the skill is currently active.
func (t *SkillTracker) IsActive(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[name]
	return ok && e.State == StateActive
}

// IsTracked returns whether the skill exists in either active or unloaded
// state.
func (t *SkillTracker) IsTracked(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.entries[name]
	return ok
}

func (t *SkillTracker) activeCountLocked() int {
	n := 0
	for _, e := range t.entries {
		if e.State == StateActive {
			n++
		}
	}
	return n
}
