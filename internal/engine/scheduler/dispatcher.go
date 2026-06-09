package scheduler

import (
	"context"
	"fmt"

	pkgtypes "harnessclaw-go/pkg/types"
)

type Dispatcher struct {
	strategies  []Strategy
	middlewares []Middleware
	deps        Deps
}

// NewDispatcher 构造 Dispatcher。
// mws 是按"执行顺序"排好的 middleware 链；调用方（通常是 wireScheduler）
// 用 middlewares.DefaultChain(deps) 拿到标准链：
//
//	Identity → AgentContext → TaskRegister → Analytics
//
// Strategy 按"注册顺序 = 优先级"传入；最后注册的通常是兜底（CanHandle 恒真）。
func NewDispatcher(deps Deps, mws []Middleware, strategies ...Strategy) *Dispatcher {
	return &Dispatcher{
		strategies:  strategies,
		middlewares: mws,
		deps:        deps,
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, p SpawnParams) (Result, error) {
	if p.Definition.Name == "" || p.Prompt == "" {
		return Result{}, ErrNilParams
	}

	strat, err := d.selectStrategy(p)
	if err != nil {
		return Result{}, err
	}

	state := &SpawnState{Strategy: strat.Name(), Bag: map[string]any{}}

	ran := 0
	for i, mw := range d.middlewares {
		var berr error
		ctx, berr = mw.Before(ctx, p, state)
		if berr != nil {
			d.unwindAfter(ctx, p, state, Result{}, berr, i-1)
			return Result{}, fmt.Errorf("middleware %s before: %w", mw.Name(), berr)
		}
		ran = i + 1
	}

	result, runErr := strat.Spawn(ctx, p, state)
	result.Strategy = strat.Name()
	result.AgentID = state.AgentID
	result.TaskID = state.TaskID

	d.unwindAfter(ctx, p, state, result, runErr, ran-1)

	for i := len(state.Cleanups) - 1; i >= 0; i-- {
		state.Cleanups[i](ctx)
	}

	return result, runErr
}

func (d *Dispatcher) Subscribe(ctx context.Context, taskID pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error) {
	info, ok := d.deps.TaskMgr.Get(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	for _, s := range d.strategies {
		if s.Name() == info.Strategy ||
			(info.Strategy == "sync→async" && s.Name() == "async") {
			return s.Subscribe(ctx, taskID)
		}
	}
	return nil, ErrNotSubscribable
}

func (d *Dispatcher) selectStrategy(p SpawnParams) (Strategy, error) {
	if p.Hints.Force != "" {
		for _, s := range d.strategies {
			if s.Name() == p.Hints.Force {
				return s, nil
			}
		}
		return nil, ErrUnknownStrategy
	}
	for _, s := range d.strategies {
		if s.CanHandle(p) {
			return s, nil
		}
	}
	return nil, ErrNoStrategy
}

func (d *Dispatcher) unwindAfter(ctx context.Context, p SpawnParams, st *SpawnState, r Result, err error, upTo int) {
	for i := upTo; i >= 0; i-- {
		d.middlewares[i].After(ctx, p, st, r, err)
	}
}

var _ Scheduler = (*Dispatcher)(nil)
