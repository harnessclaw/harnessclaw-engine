package host_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler/runtime/host"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
)

func setupReaperDeps(t *testing.T) (tstate.Kernel, msgbus.Bus) {
	t.Helper()
	tst := store.NewMemory()
	t.Cleanup(func() { tst.Close() })
	k := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("r-")})
	bus := msgbus.NewInMem(mstore.NewMemory())
	t.Cleanup(func() { bus.Close() })
	return k, bus
}

// TestReaperLeaseExpiredFiresNotify verifies that RunOnce publishes
// notify{lease_expired} for a running task whose lease has expired.
func TestReaperLeaseExpiredFiresNotify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	k, bus := setupReaperDeps(t)

	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", 10*time.Millisecond, 0) // tiny TTL

	notifyCh, cancelSub := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSub()

	time.Sleep(50 * time.Millisecond) // ensure lease expired

	r := host.NewReaper(k, bus)
	r.RunOnce(ctx)

	select {
	case msg := <-notifyCh:
		p, ok := msg.Payload.(msgbus.NotifyPayload)
		if !ok {
			t.Fatalf("expected NotifyPayload, got %T", msg.Payload)
		}
		if p.Event != msgbus.NotifyLeaseExpired {
			t.Fatalf("want lease_expired, got %s", p.Event)
		}
		if p.TaskID != string(id) {
			t.Fatalf("want task_id=%s, got %s", id, p.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("reaper did not fire notify within deadline")
	}
}

// TestReaperDeadlineExceededFiresNotify verifies notify{deadline_exceeded} for
// a running task whose absolute deadline has passed.
func TestReaperDeadlineExceededFiresNotify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	k, bus := setupReaperDeps(t)

	// Budget with a deadline already in the past
	past := time.Now().Add(-time.Millisecond)
	id, _ := k.Admit(ctx, spec.TaskSpec{
		Goal:   "deadline test",
		Budget: types.Budget{Deadline: past},
	})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0) // valid lease, but deadline already past

	notifyCh, cancelSub := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSub()

	r := host.NewReaper(k, bus)
	r.RunOnce(ctx)

	select {
	case msg := <-notifyCh:
		p, ok := msg.Payload.(msgbus.NotifyPayload)
		if !ok {
			t.Fatalf("expected NotifyPayload, got %T", msg.Payload)
		}
		if p.Event != msgbus.NotifyDeadlineExceeded {
			t.Fatalf("want deadline_exceeded, got %s", p.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("reaper did not fire deadline_exceeded notify within deadline")
	}
}

// TestReaperCancellingDrainedFiresNotify verifies that a cancelling task with no
// running children gets a notify{cancelling_drained}.
func TestReaperCancellingDrainedFiresNotify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	k, bus := setupReaperDeps(t)

	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "cancel drain test"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)
	_ = k.Cancel(ctx, id) // transitions to cancelling

	notifyCh, cancelSub := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSub()

	r := host.NewReaper(k, bus)
	r.RunOnce(ctx)

	select {
	case msg := <-notifyCh:
		p, ok := msg.Payload.(msgbus.NotifyPayload)
		if !ok {
			t.Fatalf("expected NotifyPayload, got %T", msg.Payload)
		}
		if p.Event != msgbus.NotifyCancellingDrained {
			t.Fatalf("want cancelling_drained, got %s", p.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("reaper did not fire cancelling_drained within deadline")
	}
}

// TestReaperRun verifies the background loop cancels cleanly.
func TestReaperRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	k, bus := setupReaperDeps(t)
	r := host.NewReaper(k, bus)

	err := r.Run(ctx, 50*time.Millisecond) // runs until ctx cancelled
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
