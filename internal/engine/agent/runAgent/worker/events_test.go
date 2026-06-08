package worker_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/agent/runAgent/worker"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	pkgtypes "harnessclaw-go/pkg/types"
)

// fakeReader implements just enough of tstate.Reader for EventRegistry.Find.
type fakeReader struct {
	tasks map[types.TaskID]tstate.TaskState
}

func (r *fakeReader) Get(_ context.Context, id types.TaskID) (tstate.TaskState, error) {
	if ts, ok := r.tasks[id]; ok {
		return ts, nil
	}
	return tstate.TaskState{}, nil
}
func (r *fakeReader) ListReady(_ context.Context, _ types.TeamID, _ int) ([]tstate.TaskState, error) {
	return nil, nil
}
func (r *fakeReader) ListChildren(_ context.Context, _ types.TaskID) ([]tstate.TaskState, error) {
	return nil, nil
}
func (r *fakeReader) ListByStatus(_ context.Context, _ types.TeamID, _ types.Status, _ int) ([]tstate.TaskState, error) {
	return nil, nil
}
func (r *fakeReader) ListPendingDependentOn(_ context.Context, _ types.TaskID) ([]tstate.TaskState, error) {
	return nil, nil
}

var _ tstate.Reader = (*fakeReader)(nil)

func TestEventRegistry_RegisterLookup(t *testing.T) {
	r := worker.NewEventRegistry()
	ch := make(chan pkgtypes.EngineEvent, 1)
	r.Register(types.TaskID("root"), ch)

	if got := r.Lookup(types.TaskID("root")); got == nil {
		t.Fatal("expected non-nil channel for registered ID")
	}
	if got := r.Lookup(types.TaskID("other")); got != nil {
		t.Fatal("expected nil channel for unregistered ID")
	}

	r.Unregister(types.TaskID("root"))
	if got := r.Lookup(types.TaskID("root")); got != nil {
		t.Fatal("expected nil channel after Unregister")
	}
}

func TestEventRegistry_NilSafe(t *testing.T) {
	var r *worker.EventRegistry
	r.Register(types.TaskID("x"), nil)
	r.Unregister(types.TaskID("x"))
	if r.Lookup(types.TaskID("x")) != nil {
		t.Fatal("nil registry should yield nil")
	}
	if r.Find(context.Background(), nil, types.TaskID("x")) != nil {
		t.Fatal("nil registry Find should yield nil")
	}
}

// TestEventRegistry_FindWalksParentChain registers a channel against the
// root task and verifies that Find returns it when called with a leaf
// task whose lineage chains back to that root.
func TestEventRegistry_FindWalksParentChain(t *testing.T) {
	r := worker.NewEventRegistry()
	ch := make(chan pkgtypes.EngineEvent, 1)
	r.Register(types.TaskID("root"), ch)

	reader := &fakeReader{tasks: map[types.TaskID]tstate.TaskState{
		"leaf":   {ID: "leaf", ParentID: "mid"},
		"mid":    {ID: "mid", ParentID: "root"},
		"root":   {ID: "root", ParentID: ""},
		"alone":  {ID: "alone", ParentID: ""},
	}}

	if got := r.Find(context.Background(), reader, types.TaskID("leaf")); got == nil {
		t.Fatal("expected Find to walk parent chain and locate the root channel")
	}
	if got := r.Find(context.Background(), reader, types.TaskID("alone")); got != nil {
		t.Fatal("expected Find to return nil for task with no registered ancestor")
	}
}
