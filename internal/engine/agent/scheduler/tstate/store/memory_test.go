package store_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

func TestMemoryInsertGet(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	defer s.Close()
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-1", Status: types.StatusPending, Kind: types.KindReact})
	got, err := s.Get(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-1" || got.Status != types.StatusPending {
		t.Fatalf("%+v", got)
	}
}

func TestMemoryCASRejectsMismatch(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-2", Status: types.StatusPending})
	running := types.StatusRunning
	if err := s.CAS(ctx, "t-2", types.StatusReady, types.StatusRunning, store.Mutation{Status: &running}); err == nil {
		t.Fatal("CAS should fail")
	}
}

func TestMemoryInTx(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-3", Status: types.StatusPending})
	err := s.InTx(ctx, func(tx store.Tx) error {
		ready := types.StatusReady
		return tx.CAS("t-3", types.StatusPending, types.StatusReady, store.Mutation{Status: &ready})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "t-3")
	if got.Status != types.StatusReady {
		t.Fatalf("InTx CAS did not persist")
	}
}
