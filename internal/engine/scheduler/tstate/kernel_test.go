package tstate_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func newKernel(t *testing.T) tstate.Kernel {
	t.Helper()
	st := store.NewMemory()
	t.Cleanup(func() { st.Close() })
	return tstate.NewKernel(st, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
}

func TestAdmitMarkReadyClaim(t *testing.T) {
	ctx := context.Background()
	k := newKernel(t)
	id, err := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.MarkReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := k.Claim(ctx, id, "w-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusRunning {
		t.Fatalf("want running, got %s", got.Status)
	}
}

func TestRollbackAdmitDeletes(t *testing.T) {
	ctx := context.Background()
	k := newKernel(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	if err := k.RollbackAdmit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(ctx, id); err == nil {
		t.Fatal("should have been deleted")
	}
}

func TestFailOrRetryRetryable(t *testing.T) {
	ctx := context.Background()
	k := newKernel(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x", Budget: types.Budget{MaxFailures: 2}})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", 0, 0)
	if err := k.FailOrRetry(ctx, id, types.FailLeaseExpired, "x", 0); err != nil {
		t.Fatal(err)
	}
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusReady {
		t.Fatalf("want ready (retry), got %s", got.Status)
	}
	if got.Attempt != 1 {
		t.Fatalf("attempt should be 1, got %d", got.Attempt)
	}
}

func TestConfirmSucceededFromStagingAttemptMismatch(t *testing.T) {
	ctx := context.Background()
	k := newKernel(t)
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x", Budget: types.Budget{MaxFailures: 2}})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w-1", 0, 0)
	// stale reaper saw attempt=0 and tries to confirm,
	// but a retry has since bumped attempt to 1.
	if err := k.FailOrRetry(ctx, id, types.FailLeaseExpired, "boom", 0); err != nil {
		t.Fatal(err)
	}
	if err := k.Claim(ctx, id, "w-2", 0, 1); err != nil {
		t.Fatal(err)
	}
	// stale confirm with attempt=0 must be rejected.
	if err := k.ConfirmSucceededFromStaging(ctx, id, "old-meta.json", 0); err == nil {
		t.Fatal("stale confirm with attempt=0 should error")
	}
	got, _ := k.Get(ctx, id)
	if got.Status != types.StatusRunning {
		t.Fatalf("status should remain running after rejected stale confirm, got %s", got.Status)
	}
	// fresh confirm with attempt=1 must succeed.
	if err := k.ConfirmSucceededFromStaging(ctx, id, "fresh-meta.json", 1); err != nil {
		t.Fatalf("fresh confirm should succeed: %v", err)
	}
	got, _ = k.Get(ctx, id)
	if got.Status != types.StatusSucceeded {
		t.Fatalf("status should be succeeded after fresh confirm, got %s", got.Status)
	}
	if got.ResultRef != "fresh-meta.json" {
		t.Fatalf("ResultRef should be fresh-meta.json, got %s", got.ResultRef)
	}
}

func TestStagingWriterWritesOnlyStagedRef(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	defer st.Close()
	k := tstate.NewKernel(st, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	id, _ := k.Admit(ctx, spec.TaskSpec{Goal: "x"})
	_ = k.MarkReady(ctx, id)
	_ = k.Claim(ctx, id, "w", 0, 0)
	sg := tstate.NewStagingWriter(st)
	if err := sg.StageResult(ctx, id, "meta.json", 0); err != nil {
		t.Fatal(err)
	}
	got, _ := k.Get(ctx, id)
	if got.StagedResultRef != "meta.json" {
		t.Fatalf("staged ref not written")
	}
	if got.Status != types.StatusRunning {
		t.Fatal("staging should not touch status")
	}
}
