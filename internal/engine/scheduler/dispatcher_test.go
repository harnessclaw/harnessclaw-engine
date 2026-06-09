package scheduler

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/engine/agent/definition"
	pkgtypes "harnessclaw-go/pkg/types"
)

type fakeStrategy struct {
	name      string
	canHandle bool
	spawnFn   func(ctx context.Context, p SpawnParams, st *SpawnState) (Result, error)
}

func (f fakeStrategy) Name() string                { return f.name }
func (f fakeStrategy) CanHandle(SpawnParams) bool  { return f.canHandle }
func (f fakeStrategy) Spawn(ctx context.Context, p SpawnParams, st *SpawnState) (Result, error) {
	if f.spawnFn != nil {
		return f.spawnFn(ctx, p, st)
	}
	return Result{Status: StatusCompleted, Outcome: SyncOutcome{}}, nil
}
func (f fakeStrategy) Subscribe(context.Context, pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error) {
	return nil, ErrNotSubscribable
}

type recordMW struct {
	name   string
	before func()
	after  func(error)
}

func (r recordMW) Name() string { return r.name }
func (r recordMW) Before(ctx context.Context, _ SpawnParams, _ *SpawnState) (context.Context, error) {
	if r.before != nil {
		r.before()
	}
	return ctx, nil
}
func (r recordMW) After(_ context.Context, _ SpawnParams, _ *SpawnState, _ Result, err error) {
	if r.after != nil {
		r.after(err)
	}
}

func newDispatcher(mws []Middleware, strats ...Strategy) *Dispatcher {
	d := &Dispatcher{strategies: strats}
	d.setMiddlewares(mws)
	return d
}

func makeMinimalDef() definition.AgentDefinition {
	return definition.AgentDefinition{Name: "test-agent"}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDispatcher_SelectStrategy_CanHandleOrder(t *testing.T) {
	d := newDispatcher(nil,
		fakeStrategy{name: "first", canHandle: false},
		fakeStrategy{name: "second", canHandle: true},
		fakeStrategy{name: "third", canHandle: true},
	)
	s, err := d.selectStrategy(SpawnParams{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "second" {
		t.Errorf("got %q", s.Name())
	}
}

func TestDispatcher_SelectStrategy_HintsForce(t *testing.T) {
	d := newDispatcher(nil,
		fakeStrategy{name: "sync", canHandle: true},
		fakeStrategy{name: "async", canHandle: false},
	)
	s, err := d.selectStrategy(SpawnParams{Hints: Hints{Force: "async"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "async" {
		t.Errorf("got %q", s.Name())
	}
}

func TestDispatcher_SelectStrategy_HintsForceUnknown(t *testing.T) {
	d := newDispatcher(nil, fakeStrategy{name: "sync", canHandle: true})
	_, err := d.selectStrategy(SpawnParams{Hints: Hints{Force: "nope"}})
	if !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("got %v", err)
	}
}

func TestDispatcher_SelectStrategy_NoMatch(t *testing.T) {
	d := newDispatcher(nil, fakeStrategy{name: "x", canHandle: false})
	_, err := d.selectStrategy(SpawnParams{})
	if !errors.Is(err, ErrNoStrategy) {
		t.Errorf("got %v", err)
	}
}

func TestDispatcher_Dispatch_MiddlewareOrder(t *testing.T) {
	var calls []string
	mws := []Middleware{
		recordMW{name: "a",
			before: func() { calls = append(calls, "a.before") },
			after:  func(error) { calls = append(calls, "a.after") },
		},
		recordMW{name: "b",
			before: func() { calls = append(calls, "b.before") },
			after:  func(error) { calls = append(calls, "b.after") },
		},
	}
	strat := fakeStrategy{name: "x", canHandle: true,
		spawnFn: func(ctx context.Context, _ SpawnParams, _ *SpawnState) (Result, error) {
			calls = append(calls, "strategy")
			return Result{Status: StatusCompleted, Outcome: SyncOutcome{}}, nil
		},
	}
	d := newDispatcher(mws, strat)
	_, err := d.Dispatch(context.Background(),
		SpawnParams{Prompt: "x", Definition: makeMinimalDef()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.before", "b.before", "strategy", "b.after", "a.after"}
	if !equalSlice(calls, want) {
		t.Errorf("calls = %v want %v", calls, want)
	}
}

type beforeFailMW struct {
	name string
	err  error
	log  func(string)
}

func (b beforeFailMW) Name() string { return b.name }
func (b beforeFailMW) Before(ctx context.Context, _ SpawnParams, _ *SpawnState) (context.Context, error) {
	b.log(b.name + ".before(fail)")
	return ctx, b.err
}
func (b beforeFailMW) After(context.Context, SpawnParams, *SpawnState, Result, error) {}

func TestDispatcher_Dispatch_BeforeFailUnwinds(t *testing.T) {
	var calls []string
	boom := errors.New("boom")
	mws := []Middleware{
		recordMW{name: "a",
			before: func() { calls = append(calls, "a.before") },
			after:  func(error) { calls = append(calls, "a.after") },
		},
		beforeFailMW{name: "b", err: boom, log: func(s string) { calls = append(calls, s) }},
		recordMW{name: "c",
			before: func() { calls = append(calls, "c.before") },
			after:  func(error) { calls = append(calls, "c.after") },
		},
	}
	strat := fakeStrategy{name: "x", canHandle: true,
		spawnFn: func(context.Context, SpawnParams, *SpawnState) (Result, error) {
			calls = append(calls, "strategy")
			return Result{}, nil
		},
	}
	d := newDispatcher(mws, strat)
	_, err := d.Dispatch(context.Background(), SpawnParams{Prompt: "x", Definition: makeMinimalDef()})
	if err == nil {
		t.Fatal("want error")
	}
	// 期望：a.before / b.before(fail) / a.after（逆序回滚），c.before 不跑，strategy 不跑
	want := []string{"a.before", "b.before(fail)", "a.after"}
	if !equalSlice(calls, want) {
		t.Errorf("calls = %v want %v", calls, want)
	}
}

func TestDispatcher_Dispatch_NilParams(t *testing.T) {
	d := newDispatcher(nil, fakeStrategy{name: "x", canHandle: true})
	_, err := d.Dispatch(context.Background(), SpawnParams{}) // empty Definition.Name + empty Prompt
	if !errors.Is(err, ErrNilParams) {
		t.Errorf("want ErrNilParams, got %v", err)
	}
}
