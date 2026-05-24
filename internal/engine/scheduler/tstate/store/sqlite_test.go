package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func newSQLiteStore(t *testing.T) tstate.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s, err := store.NewSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		db.Close()
	})
	return s
}

func TestSQLiteInsertGet(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{
		ID:        "t-1",
		Status:    types.StatusPending,
		Kind:      types.KindLeaf,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Insert(ctx, ts); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-1" {
		t.Fatalf("want t-1, got %s", got.ID)
	}
	if got.Status != types.StatusPending {
		t.Fatalf("want pending, got %s", got.Status)
	}
	if got.Kind != types.KindLeaf {
		t.Fatalf("want leaf, got %s", got.Kind)
	}
}

func TestSQLiteCASTransition(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	st := types.StatusReady
	if err := s.CAS(ctx, "t-1", types.StatusPending, types.StatusReady, tstate.Mutation{Status: &st}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "t-1")
	if got.Status != types.StatusReady {
		t.Fatalf("want ready, got %s", got.Status)
	}
}

func TestSQLiteCASMismatch(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	st := types.StatusRunning
	err := s.CAS(ctx, "t-1", types.StatusReady, types.StatusRunning, tstate.Mutation{Status: &st})
	if err == nil {
		t.Fatal("expected CAS conflict error")
	}
}

func TestSQLiteDeleteNotFound(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	err := s.Delete(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing row")
	}
}

func TestSQLiteDeleteExists(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	if err := s.Delete(ctx, "t-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "t-1"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestSQLiteInTxRollbackOnError(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	_ = s.InTx(ctx, func(tx tstate.Tx) error {
		st := types.StatusReady
		_ = tx.CAS("t-1", types.StatusPending, types.StatusReady, tstate.Mutation{Status: &st})
		return fmt.Errorf("rollback me")
	})
	got, _ := s.Get(ctx, "t-1")
	if got.Status != types.StatusPending {
		t.Fatalf("want rollback to pending, got %s", got.Status)
	}
}

func TestSQLiteInTxCommit(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	err := s.InTx(ctx, func(tx tstate.Tx) error {
		st := types.StatusReady
		return tx.CAS("t-1", types.StatusPending, types.StatusReady, tstate.Mutation{Status: &st})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "t-1")
	if got.Status != types.StatusReady {
		t.Fatalf("want ready, got %s", got.Status)
	}
}

func TestSQLiteListByStatus(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	team := types.TeamID("team-1")
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-1", TeamID: team, Status: types.StatusPending})
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-2", TeamID: team, Status: types.StatusReady})
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-3", TeamID: team, Status: types.StatusPending})

	results, err := s.ListByStatus(ctx, team, types.StatusPending, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 pending, got %d", len(results))
	}
}

func TestSQLiteListByParent(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	parent := types.TaskID("t-parent")
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-1", ParentID: parent, Status: types.StatusPending})
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-2", ParentID: parent, Status: types.StatusReady})
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-3", ParentID: "other", Status: types.StatusPending})

	results, err := s.ListByParent(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 children, got %d", len(results))
	}
}

func TestSQLiteListPendingDependentOn(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	dep := types.TaskID("t-dep")
	_ = s.Insert(ctx, tstate.TaskState{
		ID:     "t-1",
		Status: types.StatusPending,
		Deps:   []types.TaskID{dep, "t-other"},
	})
	_ = s.Insert(ctx, tstate.TaskState{
		ID:     "t-2",
		Status: types.StatusPending,
		Deps:   []types.TaskID{"t-other"},
	})
	_ = s.Insert(ctx, tstate.TaskState{
		ID:     "t-3",
		Status: types.StatusReady,
		Deps:   []types.TaskID{dep},
	})

	results, err := s.ListPendingDependentOn(ctx, dep)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 pending dependent, got %d", len(results))
	}
	if results[0].ID != "t-1" {
		t.Fatalf("want t-1, got %s", results[0].ID)
	}
}

func TestSQLiteUpdateFieldStagedResultRef(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusRunning, Attempt: 1}
	_ = s.Insert(ctx, ts)

	ref := types.Ref("blob://staged/result")
	if err := s.UpdateField(ctx, "t-1", tstate.FieldStagedResultRef, ref, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "t-1")
	if got.StagedResultRef != ref {
		t.Fatalf("want %s, got %s", ref, got.StagedResultRef)
	}
}

func TestSQLiteUpdateFieldAttemptMismatch(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusRunning, Attempt: 1}
	_ = s.Insert(ctx, ts)

	ref := types.Ref("blob://staged/result")
	err := s.UpdateField(ctx, "t-1", tstate.FieldStagedResultRef, ref, 99)
	if err == nil {
		t.Fatal("expected epoch mismatch error")
	}
}

func TestSQLiteInTxGetAndListChildren(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	parent := types.TaskID("p-1")
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-1", ParentID: parent, Status: types.StatusPending})
	_ = s.Insert(ctx, tstate.TaskState{ID: "t-2", ParentID: parent, Status: types.StatusPending})

	err := s.InTx(ctx, func(tx tstate.Tx) error {
		got, err := tx.Get("t-1")
		if err != nil {
			return err
		}
		if got.ID != "t-1" {
			return fmt.Errorf("want t-1, got %s", got.ID)
		}
		children, err := tx.ListChildren(parent)
		if err != nil {
			return err
		}
		if len(children) != 2 {
			return fmt.Errorf("want 2 children, got %d", len(children))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteDuplicateInsert(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteStore(t)
	ts := tstate.TaskState{ID: "t-1", Status: types.StatusPending}
	_ = s.Insert(ctx, ts)
	err := s.Insert(ctx, ts)
	if err == nil {
		t.Fatal("expected duplicate insert error")
	}
}
