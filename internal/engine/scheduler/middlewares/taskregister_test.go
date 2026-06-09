package middlewares

import (
	"context"
	"errors"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/tasks"
)

func TestTaskRegister_SkipsSync(t *testing.T) {
	mgr := tasks.NewMemory()
	mw := TaskRegister{Mgr: mgr}
	st := &scheduler.SpawnState{Strategy: "sync", AgentID: "a-1", TaskID: "t-1", Bag: map[string]any{}}
	_, err := mw.Before(context.Background(), scheduler.SpawnParams{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if list := mgr.List(); len(list) != 0 {
		t.Errorf("sync 不应预注册；got %d 个 task", len(list))
	}
}

func TestTaskRegister_RegistersAsync(t *testing.T) {
	mgr := tasks.NewMemory()
	mw := TaskRegister{Mgr: mgr}
	st := &scheduler.SpawnState{Strategy: "async", AgentID: "a-1", TaskID: "t-1", Bag: map[string]any{}}
	p := scheduler.SpawnParams{Name: "alice"}
	_, err := mw.Before(context.Background(), p, st)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := mgr.Get("t-1")
	if !ok {
		t.Fatal("async 应该被预注册")
	}
	if info.Name != "alice" {
		t.Errorf("Name got %q", info.Name)
	}
}

func TestTaskRegister_After_MarksLaunchFailed(t *testing.T) {
	mgr := tasks.NewMemory()
	mw := TaskRegister{Mgr: mgr}
	st := &scheduler.SpawnState{Strategy: "async", AgentID: "a-1", TaskID: "t-1", Bag: map[string]any{}}
	_, _ = mw.Before(context.Background(), scheduler.SpawnParams{}, st)

	boom := errors.New("boom")
	mw.After(context.Background(), scheduler.SpawnParams{}, st, scheduler.Result{}, boom)

	info, _ := mgr.Get("t-1")
	if info.Status != tasks.TaskFailed {
		t.Errorf("Status got %q want failed", info.Status)
	}
}

var _ = time.Now
