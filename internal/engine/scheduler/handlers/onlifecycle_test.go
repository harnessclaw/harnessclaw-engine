package handlers_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler/audit"
	"harnessclaw-go/internal/engine/scheduler/handlers"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
)

func setup(t *testing.T) (tstate.Kernel, msgbus.Bus, *handlers.OnLifecycleHandler) {
	t.Helper()
	tst := store.NewMemory()
	t.Cleanup(func() { tst.Close() })
	k := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	bus := msgbus.NewInMem(mstore.NewMemory())
	t.Cleanup(func() { bus.Close() })
	au := audit.NewSlogLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	h := handlers.NewOnLifecycle(k, k, bus, au)
	return k, bus, h
}

func TestOnLifecycleDropStaleAttempt(t *testing.T) {
	ctx := context.Background()
	k, bus, h := setup(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)
	// Fake completion with WRONG attempt
	h.Handle(ctx, msgbus.AgentMessage{
		MsgID: "m1", Kind: msgbus.KindLifecycle,
		From: msgbus.AddrAgent(string(id)), TaskID: string(id),
		Payload: msgbus.LifecyclePayload{Event: msgbus.EventCompleted, Attempt: 99, ResultRef: "x"},
	})
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusRunning {
		t.Fatalf("stale attempt should NOT advance state; got %s", got.Status)
	}
	_ = bus // suppress unused
}

func TestOnLifecycleDropBadFrom(t *testing.T) {
	ctx := context.Background()
	k, _, h := setup(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)
	// From doesn't match agent:<task_id>
	h.Handle(ctx, msgbus.AgentMessage{
		MsgID: "m2", Kind: msgbus.KindLifecycle,
		From: "reaper", TaskID: string(id),
		Payload: msgbus.LifecyclePayload{Event: msgbus.EventCompleted, Attempt: 0, ResultRef: "x"},
	})
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusRunning {
		t.Fatalf("bad From should NOT advance state; got %s", got.Status)
	}
}

func TestOnLifecycleCompletedSucceeds(t *testing.T) {
	ctx := context.Background()
	k, _, h := setup(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", time.Minute, 0)
	h.Handle(ctx, msgbus.AgentMessage{
		MsgID: "m3", Kind: msgbus.KindLifecycle,
		From: msgbus.AddrAgent(string(id)), TaskID: string(id),
		Payload: msgbus.LifecyclePayload{Event: msgbus.EventCompleted, Attempt: 0, ResultRef: "meta.json"},
	})
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusSucceeded {
		t.Fatalf("want succeeded, got %s", got.Status)
	}
	if got.ResultRef != "meta.json" {
		t.Fatalf("ResultRef not set")
	}
}
