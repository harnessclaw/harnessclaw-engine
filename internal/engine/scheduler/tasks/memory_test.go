package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

func TestMemory_Register_GetList(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	err := m.Register(ctx, RegisterParams{
		TaskID: "t-1", AgentID: "a-1", Name: "alice", Strategy: "async",
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	info, ok := m.Get("t-1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	if info.AgentID != "a-1" {
		t.Errorf("AgentID got %q", info.AgentID)
	}
	if info.Status != TaskRunning {
		t.Errorf("Status got %q want running", info.Status)
	}

	if list := m.List(); len(list) != 1 {
		t.Errorf("List len got %d want 1", len(list))
	}
}

func TestMemory_Complete(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_ = m.Register(ctx, RegisterParams{TaskID: "t-1", AgentID: "a-1", StartedAt: time.Now()})
	if err := m.Complete(ctx, "t-1"); err != nil {
		t.Fatal(err)
	}
	info, _ := m.Get("t-1")
	if info.Status != TaskCompleted {
		t.Errorf("Status got %q want completed", info.Status)
	}
}

func TestMemory_Fail(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_ = m.Register(ctx, RegisterParams{TaskID: "t-1", AgentID: "a-1", StartedAt: time.Now()})
	sentinel := errors.New("boom")
	if err := m.Fail(ctx, "t-1", sentinel); err != nil {
		t.Fatal(err)
	}
	info, _ := m.Get("t-1")
	if info.Status != TaskFailed {
		t.Errorf("Status got %q want failed", info.Status)
	}
	if info.LastError != "boom" {
		t.Errorf("LastError got %q", info.LastError)
	}
}

func TestMemory_Tick_BumpsActivity(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_ = m.Register(ctx, RegisterParams{TaskID: "t-1", AgentID: "a-1", StartedAt: time.Now()})
	before, _ := m.Get("t-1")
	time.Sleep(2 * time.Millisecond)
	_ = m.Tick(ctx, "t-1", pkgtypes.EngineEvent{Type: "test"})
	after, _ := m.Get("t-1")
	if !after.LastActivityAt.After(before.LastActivityAt) {
		t.Error("LastActivityAt should bump")
	}
	if after.EventCount != 1 {
		t.Errorf("EventCount got %d want 1", after.EventCount)
	}
}

func TestMemory_ForegroundBgSignal(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	bg := m.RegisterForeground(ctx, "t-1", "a-1")
	select {
	case <-bg:
		t.Fatal("bgSignal should not fire before RequestBackground")
	default:
	}
	m.RequestBackground("t-1")
	select {
	case <-bg:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("bgSignal should fire after RequestBackground")
	}
}

func TestMemory_Wait_UntilTerminal(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_ = m.Register(ctx, RegisterParams{TaskID: "t-1", AgentID: "a-1", StartedAt: time.Now()})

	done := make(chan TaskInfo, 1)
	go func() {
		info, _ := m.Wait(context.Background(), "t-1")
		done <- info
	}()
	time.Sleep(5 * time.Millisecond)
	_ = m.Complete(ctx, "t-1")

	select {
	case info := <-done:
		if info.Status != TaskCompleted {
			t.Errorf("got %q", info.Status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wait should return after Complete")
	}
}

func TestMemory_Cancel(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_ = m.Register(ctx, RegisterParams{TaskID: "t-1", AgentID: "a-1", StartedAt: time.Now()})
	if err := m.Cancel(ctx, "t-1"); err != nil {
		t.Fatal(err)
	}
	info, _ := m.Get("t-1")
	if info.Status != TaskCancelled {
		t.Errorf("got %q", info.Status)
	}
}
