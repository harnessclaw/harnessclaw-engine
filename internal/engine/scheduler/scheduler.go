// Package scheduler is the L2 top-level entry point.
// Scheduler.New wires all handlers, strategies, and the message router,
// then exposes Submit/Start/Cancel for L1 callers.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"harnessclaw-go/internal/engine/scheduler/audit"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/dispatch/plan"
	"harnessclaw-go/internal/engine/scheduler/dispatch/react"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/scheduler/runtime/host"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// Config carries the external dependencies for Scheduler.New.
type Config struct {
	// Logger is the service-wide logger. Required.
	Logger *slog.Logger

	// Bus is the message bus used by all handlers and strategies.
	Bus msgbus.Bus

	// Kernel is the read+write tstate interface. Held only by the scheduler and handlers.
	Kernel tstate.Kernel

	// Staging is the narrow write interface for staging result refs.
	Staging tstate.StagingWriter

	// ReactCaps are the Capabilities for the react strategy.
	// Defaults (LeafKind, AllowedTools) are applied in react.New.
	ReactCaps dispatch.Capabilities

	// PlanCaps are the Capabilities for the plan strategy.
	// AllowSubmit is forced true in plan.New.
	PlanCaps dispatch.Capabilities
}

// Scheduler is the L2 scheduler. Create with New; call Start before Submit.
type Scheduler struct {
	cfg        Config
	strategies map[types.Kind]dispatch.Strategy
	stratHost  *host.StrategyHost
	log        audit.Logger
}

// New wires all strategies and returns a Scheduler ready for Start.
func New(cfg Config) *Scheduler {
	au := audit.NewSlogLogger(cfg.Logger)

	strategies := []dispatch.Strategy{
		react.New(cfg.ReactCaps),
		plan.New(cfg.PlanCaps),
	}
	stratMap := make(map[types.Kind]dispatch.Strategy, len(strategies))
	for _, s := range strategies {
		stratMap[s.Kind()] = s
	}

	sh := host.NewStrategyHost(cfg.Kernel, cfg.Bus, cfg.Staging, strategies)

	return &Scheduler{
		cfg:        cfg,
		strategies: stratMap,
		stratHost:  sh,
		log:        au,
	}
}

// Start subscribes all handlers to the bus via runtime.Handle.
// It spawns a background goroutine that processes messages until ctx is cancelled.
// Call Start exactly once before any Submit calls.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		if err := runtime.Handle(ctx, s.cfg.Kernel, s.cfg.Bus, s.log, msgbus.AddrScheduler); err != nil {
			if ctx.Err() == nil {
				s.log.Log(ctx, "scheduler_runtime_error", slog.Attr{
					Key:   "error",
					Value: slog.StringValue(err.Error()),
				})
			}
		}
	}()
}

// Submit admits a task and starts its strategy host goroutines.
// Returns the assigned TaskID. The task runs asynchronously; callers can
// observe progress via Bus subscriptions or by polling the Kernel.
func (s *Scheduler) Submit(ctx context.Context, sp spec.TaskSpec) (types.TaskID, error) {
	// Validate strategy exists.
	if _, ok := s.strategies[sp.Hint.Kind]; !ok {
		return "", fmt.Errorf("scheduler: Submit: unknown kind %q", sp.Hint.Kind)
	}

	// Admit: inserts a pending row in tstate.
	taskID, err := s.cfg.Kernel.Admit(ctx, sp)
	if err != nil {
		return "", fmt.Errorf("scheduler: Submit: admit: %w", err)
	}

	// No deps → transition to ready immediately.
	if len(sp.Deps) == 0 {
		if err := s.cfg.Kernel.MarkReady(ctx, taskID); err != nil {
			_ = s.cfg.Kernel.RollbackAdmit(ctx, taskID)
			return "", fmt.Errorf("scheduler: Submit: mark_ready: %w", err)
		}
	}

	// Fork G1+G2 goroutines for the strategy.
	if err := s.stratHost.RunTask(ctx, taskID); err != nil {
		_ = s.cfg.Kernel.RollbackAdmit(ctx, taskID)
		return "", fmt.Errorf("scheduler: Submit: run_task: %w", err)
	}

	return taskID, nil
}

// Cancel requests cancellation of a task and all its descendants.
// It transitions the task to "cancelling" in tstate and publishes a
// control{cancel} message to the agent's mailbox.
func (s *Scheduler) Cancel(ctx context.Context, id types.TaskID) error {
	if err := s.cfg.Kernel.Cancel(ctx, id); err != nil {
		return fmt.Errorf("scheduler: Cancel: %w", err)
	}
	return s.cfg.Bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   "cancel:" + string(id),
		Kind:    msgbus.KindControl,
		From:    msgbus.AddrScheduler,
		To:      msgbus.AddrAgent(string(id)),
		TaskID:  string(id),
		Payload: msgbus.ControlPayload{Cmd: msgbus.CmdCancel},
	})
}
