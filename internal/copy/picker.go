package copy

import (
	"math/rand"
	"sync"

	emitv2 "harnessclaw-go/internal/emit/v2"
)

// sessionCap caps the number of distinct sessions tracked in memory.
// Beyond this, the oldest session's rotation state is evicted (LRU).
// 1000 is generous for a single-tenant deployment; multi-tenant should
// tune via NewCopyPickerWithCap.
const sessionCap = 1000

// CopyPicker resolves a (toolName, phase) query to a localized
// user-facing string, with session-level rotation so the same user
// doesn't see the same phrase twice in a row for the same key.
//
// Concurrency-safe: a single instance can serve many sessions.
type CopyPicker struct {
	mu       sync.Mutex
	cap      int
	sessions map[string]*sessionPickerState
	order    []string // LRU order, head = oldest
	rngFunc  func() *rand.Rand
}

type copyKey struct {
	Category ToolCategory
	Phase    emitv2.ToolPhase
}

type sessionPickerState struct {
	used map[copyKey]map[int]bool
	rng  *rand.Rand
}

// NewCopyPicker builds a Picker with the given RNG factory. Production
// callers pass `func() *rand.Rand { return rand.New(rand.NewSource(time.Now().UnixNano())) }`;
// tests pass a fixed-seed factory for deterministic assertions.
func NewCopyPicker(rngFunc func() *rand.Rand) *CopyPicker {
	return NewCopyPickerWithCap(rngFunc, sessionCap)
}

// NewCopyPickerWithCap exposes the session cap for tests / specialized
// deployments. Use the default sessionCap (1000) in production.
func NewCopyPickerWithCap(rngFunc func() *rand.Rand, cap int) *CopyPicker {
	if cap <= 0 {
		cap = sessionCap
	}
	return &CopyPicker{
		cap:      cap,
		sessions: make(map[string]*sessionPickerState),
		order:    make([]string, 0, cap),
		rngFunc:  rngFunc,
	}
}

// Pick returns a resolved copy string for (toolName, phase). Bytes is
// only meaningful for PhasePlanningArgs. RetryInfo is only meaningful
// for retry-templated phases. Empty result means no template registered
// — caller may render a Phase-enum-based default in the front-end.
func (p *CopyPicker) Pick(sessionID, toolName string, phase emitv2.ToolPhase, bytes int, retry *RetryInfo) string {
	cat := Categorize(toolName)
	templates := lookup(cat, phase)
	if len(templates) == 0 {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	st := p.getOrInitSession(sessionID)
	key := copyKey{cat, phase}

	if len(st.used[key]) >= len(templates) {
		st.used[key] = map[int]bool{}
	}
	if st.used[key] == nil {
		st.used[key] = map[int]bool{}
	}

	var idx int
	for {
		idx = st.rng.Intn(len(templates))
		if !st.used[key][idx] {
			break
		}
	}
	st.used[key][idx] = true

	return interpolate(templates[idx], bytes, retry)
}

// Forget drops all rotation state for sessionID. Channel layer calls
// this on EngineEventDone / session disconnect to prevent the
// sessions map from growing unbounded across the server's lifetime.
func (p *CopyPicker) Forget(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sessions[sessionID]; !ok {
		return
	}
	delete(p.sessions, sessionID)
	for i, id := range p.order {
		if id == sessionID {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

// activeSessionCount is a test helper.
func (p *CopyPicker) activeSessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// getOrInitSession returns the per-session state, creating it on first
// access. Enforces the LRU cap by evicting the oldest entry when the
// map is full. Caller must hold p.mu.
func (p *CopyPicker) getOrInitSession(sessionID string) *sessionPickerState {
	if st, ok := p.sessions[sessionID]; ok {
		for i, id := range p.order {
			if id == sessionID {
				p.order = append(p.order[:i], p.order[i+1:]...)
				break
			}
		}
		p.order = append(p.order, sessionID)
		return st
	}

	for len(p.sessions) >= p.cap {
		if len(p.order) == 0 {
			break
		}
		evict := p.order[0]
		p.order = p.order[1:]
		delete(p.sessions, evict)
	}

	st := &sessionPickerState{
		used: map[copyKey]map[int]bool{},
		rng:  p.rngFunc(),
	}
	p.sessions[sessionID] = st
	p.order = append(p.order, sessionID)
	return st
}
