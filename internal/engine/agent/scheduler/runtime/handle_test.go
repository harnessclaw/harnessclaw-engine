package runtime_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/audit"
	"harnessclaw-go/internal/engine/agent/scheduler/runtime"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
)

func setupRuntime(t *testing.T) (tstate.Kernel, msgbus.Bus, audit.Logger) {
	t.Helper()
	tst := store.NewMemory()
	t.Cleanup(func() { tst.Close() })
	k := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	bus := msgbus.NewInMem(mstore.NewMemory())
	t.Cleanup(func() { bus.Close() })
	log := audit.NewSlogLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return k, bus, log
}

// TestHandleLifecycleRouting verifies that a KindLifecycle{completed} message
// published to AddrScheduler is routed to onLifecycle and transitions the task
// to succeeded.
func TestHandleLifecycleRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	k, bus, log := setupRuntime(t)

	// Set up a task in running state
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "test routing"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)

	// Run Handle in a goroutine — it blocks until ctx is done
	ready := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Handle(ctx, k, bus, log, msgbus.AddrScheduler, ready)
	}()
	<-ready // wait for subscription to be established

	// Publish a KindLifecycle{completed} message addressed to scheduler
	_ = bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:  "test-lifecycle-1",
		Kind:   msgbus.KindLifecycle,
		From:   msgbus.AddrAgent(string(id)),
		To:     msgbus.AddrScheduler,
		TaskID: string(id),
		Payload: msgbus.LifecyclePayload{
			Event: msgbus.EventCompleted, Attempt: 0, ResultRef: "meta.json",
		},
	})

	// Poll for task to become succeeded (max ~1s)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ts, _ := k.Get(ctx, id)
		if ts.Status == types.StatusSucceeded {
			return // test passes
		}
		time.Sleep(20 * time.Millisecond)
	}

	ts, _ := k.Get(ctx, id)
	t.Fatalf("task should be succeeded after lifecycle{completed}; got status=%s", ts.Status)
}

// TestHandleNotifySubrouting verifies that KindNotify{lease_expired} is
// dispatched to onExpire and not silently dropped.
func TestHandleNotifySubrouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	k, bus, log := setupRuntime(t)

	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "expire test"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)

	ready2 := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Handle(ctx, k, bus, log, msgbus.AddrScheduler, ready2)
	}()
	<-ready2 // wait for subscription

	_ = bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:  "test-expire-1",
		Kind:   msgbus.KindNotify,
		From:   msgbus.AddrReaper,
		To:     msgbus.AddrScheduler,
		TaskID: string(id),
		Payload: msgbus.NotifyPayload{
			Event:  msgbus.NotifyLeaseExpired,
			TaskID: string(id),
		},
	})

	// onExpire calls Expire → FailOrRetry; task should no longer be running
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ts, _ := k.Get(ctx, id)
		if ts.Status != types.StatusRunning {
			return // transitioned — test passes
		}
		time.Sleep(20 * time.Millisecond)
	}

	ts, _ := k.Get(ctx, id)
	t.Fatalf("task should have transitioned from running after lease_expired; got %s", ts.Status)
}
