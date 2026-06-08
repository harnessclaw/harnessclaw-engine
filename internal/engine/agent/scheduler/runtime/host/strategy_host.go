// Package host contains the per-task runtime harness: StrategyHost (G1+G2) and
// the background Reaper.
package host

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/dispatch"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// RunFunc is the strategy entry point: dispatch.Strategy.Run wrapped for StrategyHost.
type RunFunc func(ctx context.Context) (types.MetaRef, error)

const defaultHeartbeatTTL = 30 * time.Second

// StrategyHost runs one task via its appropriate Strategy.
// It holds the kernel (read-only via Reader), the bus for publishing lifecycle events,
// a map of strategies by Kind, and the staging writer for the crash-recovery fallback.
type StrategyHost struct {
	kernel     tstate.Reader
	bus        msgbus.Bus
	staging    tstate.StagingWriter
	strategies map[types.Kind]dispatch.Strategy
	log        interface{ Log(ctx context.Context, event string, attrs ...interface{}) }
}

// NewStrategyHost constructs a StrategyHost.
func NewStrategyHost(
	kernel tstate.Reader,
	bus msgbus.Bus,
	staging tstate.StagingWriter,
	strategies []dispatch.Strategy,
) *StrategyHost {
	m := make(map[types.Kind]dispatch.Strategy, len(strategies))
	for _, s := range strategies {
		m[s.Kind()] = s
	}
	return &StrategyHost{
		kernel:     kernel,
		bus:        bus,
		staging:    staging,
		strategies: m,
	}
}

// RunTask looks up the strategy for the task's Kind, then calls StartStrategyHost.
// It returns immediately after launching the goroutines.
func (h *StrategyHost) RunTask(ctx context.Context, taskID types.TaskID) error {
	ts, err := h.kernel.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("strategy_host: get task %s: %w", taskID, err)
	}
	strategy, ok := h.strategies[ts.Kind]
	if !ok {
		return fmt.Errorf("strategy_host: no strategy for kind %q (task %s)", ts.Kind, taskID)
	}

	attempt := ts.Attempt
	deps := dispatch.Deps{
		Reader:  h.kernel,
		Bus:     h.bus,
		Staging: h.staging,
	}
	run := func(ctx context.Context) (types.MetaRef, error) {
		return strategy.Run(ctx, taskID, deps)
	}

	return StartStrategyHost(ctx, h.bus, h.staging, taskID, attempt, run)
}

// StartStrategyHost forks G1 (strategy execution) + G2 (heartbeat).
// G1 emits lifecycle{started}, runs run(ctx), then publishes lifecycle{completed|failed}.
// G2 sends periodic lifecycle{heartbeat} while G1 is running.
// Returns the Handle immediately; the goroutines proceed asynchronously.
func StartStrategyHost(
	ctx context.Context,
	bus msgbus.Bus,
	staging tstate.StagingWriter,
	taskID types.TaskID,
	attempt int,
	run RunFunc,
) error {
	addr := msgbus.AddrAgent(string(taskID))

	go func() {
		// G1: lifecycle{started}
		_ = bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:   fmt.Sprintf("life:%s:start:%d", taskID, attempt),
			Kind:    msgbus.KindLifecycle,
			From:    addr,
			To:      msgbus.AddrScheduler,
			TaskID:  string(taskID),
			Payload: msgbus.LifecyclePayload{Event: msgbus.EventStarted, Attempt: attempt},
		})

		// G2: heartbeat loop
		hbCtx, stopHB := context.WithCancel(ctx)
		go heartbeatLoop(hbCtx, bus, taskID, attempt, defaultHeartbeatTTL)
		defer stopHB()

		// Run strategy, recovering from panics
		metaRef, runErr := safeRun(ctx, run)

		if runErr == nil {
			// Stage before publishing completed (crash-recovery fallback, spec §6.2.1)
			_ = staging.StageResult(ctx, taskID, types.Ref(metaRef), attempt)
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  fmt.Sprintf("life:%s:complete:%d", taskID, attempt),
				Kind:   msgbus.KindLifecycle,
				From:   addr,
				To:     msgbus.AddrScheduler,
				TaskID: string(taskID),
				Payload: msgbus.LifecyclePayload{
					Event:     msgbus.EventCompleted,
					Attempt:   attempt,
					ResultRef: string(metaRef),
				},
			})
		} else {
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  fmt.Sprintf("life:%s:fail:%d", taskID, attempt),
				Kind:   msgbus.KindLifecycle,
				From:   addr,
				To:     msgbus.AddrScheduler,
				TaskID: string(taskID),
				Payload: msgbus.LifecyclePayload{
					Event:         msgbus.EventFailed,
					Attempt:       attempt,
					FailureReason: string(types.FailWorkerError),
					ErrMsg:        truncate(runErr.Error(), 4096),
				},
			})
		}
	}()

	return nil
}

func heartbeatLoop(ctx context.Context, bus msgbus.Bus, taskID types.TaskID, attempt int, ttl time.Duration) {
	interval := ttl / 3
	if interval <= 0 {
		interval = 10 * time.Second
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	addr := msgbus.AddrAgent(string(taskID))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  fmt.Sprintf("life:%s:hb:%d:%d", taskID, attempt, time.Now().UnixNano()),
				Kind:   msgbus.KindLifecycle,
				From:   addr,
				To:     msgbus.AddrScheduler,
				TaskID: string(taskID),
				Payload: msgbus.LifecyclePayload{
					Event:   msgbus.EventHeartbeat,
					Attempt: attempt,
				},
			})
		}
	}
}

func safeRun(ctx context.Context, run RunFunc) (ref types.MetaRef, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, truncate(string(debug.Stack()), 4096))
		}
	}()
	return run(ctx)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
