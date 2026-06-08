package worker

import (
	"context"
	"sync"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	pkgtypes "harnessclaw-go/pkg/types"
)

// EventRegistry maps a "root" task ID (the one returned by Scheduler.Submit)
// to the per-Run event channel the caller supplied. The Factory uses it
// to forward L3 sub-agent lifecycle events back to the original client
// stream without mutating package-global state.
//
// Replaces the legacy QueryEngineFactory.SetOutCh / currentOutCh global
// holders, which clobbered each other under concurrent Run calls.
type EventRegistry struct {
	mu  sync.RWMutex
	chs map[types.TaskID]chan<- pkgtypes.EngineEvent
}

// NewEventRegistry constructs an empty registry. Safe for concurrent use.
func NewEventRegistry() *EventRegistry {
	return &EventRegistry{chs: make(map[types.TaskID]chan<- pkgtypes.EngineEvent)}
}

// Register associates id with ch. ch is nil-tolerant — registering nil is
// a no-op so callers can wrap optional channels without branching.
func (r *EventRegistry) Register(id types.TaskID, ch chan<- pkgtypes.EngineEvent) {
	if r == nil || id == "" || ch == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chs[id] = ch
}

// Unregister removes id from the registry. Safe to call even when id was
// never registered.
func (r *EventRegistry) Unregister(id types.TaskID) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chs, id)
}

// Lookup returns the channel registered exactly for id, or nil. It does
// NOT walk parent chains — use Find for lineage-aware lookup.
func (r *EventRegistry) Lookup(id types.TaskID) chan<- pkgtypes.EngineEvent {
	if r == nil || id == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.chs[id]
}

// Find walks the parent chain of taskID via reader and returns the first
// registered channel found, or nil if no ancestor (including taskID itself)
// is registered. Loop detection caps the walk at 32 hops.
func (r *EventRegistry) Find(ctx context.Context, reader tstate.Reader, taskID types.TaskID) chan<- pkgtypes.EngineEvent {
	if r == nil || reader == nil || taskID == "" {
		return nil
	}
	cur := taskID
	for hop := 0; hop < 32 && cur != ""; hop++ {
		if ch := r.Lookup(cur); ch != nil {
			return ch
		}
		ts, err := reader.Get(ctx, cur)
		if err != nil {
			return nil
		}
		if ts.ParentID == "" || ts.ParentID == cur {
			return nil
		}
		cur = ts.ParentID
	}
	return nil
}
