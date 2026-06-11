package async

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/diskout"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/scheduler/tasks"
	pkgtypes "harnessclaw-go/pkg/types"
)

type fakeRuntime struct{ events []pkgtypes.EngineEvent }

func (f *fakeRuntime) Run(_ context.Context, _ runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	ch := make(chan pkgtypes.EngineEvent, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestAsync_CanHandle_BackgroundHint(t *testing.T) {
	s := &Strategy{}
	if !s.CanHandle(scheduler.SpawnParams{Hints: scheduler.Hints{Background: true}}) {
		t.Error("Background=true should match")
	}
	if s.CanHandle(scheduler.SpawnParams{}) {
		t.Error("default should not match")
	}
}

func TestAsync_Spawn_ReturnsAsyncOutcome(t *testing.T) {
	mgr := tasks.NewMemory()
	s := &Strategy{
		rt:      &fakeRuntime{events: []pkgtypes.EngineEvent{{Type: pkgtypes.EngineEventType("a")}, {Type: pkgtypes.EngineEventType("b")}}},
		taskMgr: mgr,
		diskOut: diskout.NewFS(t.TempDir()),
	}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "async", Bag: map[string]any{}}
	_ = mgr.Register(context.Background(), tasks.RegisterParams{
		TaskID: "t-1", AgentID: "a-1", Strategy: "async", StartedAt: time.Now(),
	})

	res, err := s.Spawn(context.Background(), scheduler.SpawnParams{Prompt: "hi"}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scheduler.StatusAsyncLaunched {
		t.Errorf("Status got %q", res.Status)
	}
	out, ok := res.Outcome.(scheduler.AsyncOutcome)
	if !ok {
		t.Fatalf("Outcome type %T", res.Outcome)
	}
	if out.OutputFile == "" {
		t.Error("OutputFile empty")
	}

	// 等后台 goroutine 跑完
	final, err := mgr.Wait(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != tasks.TaskCompleted {
		t.Errorf("final status %q", final.Status)
	}
}

type slowRuntime struct{ events chan pkgtypes.EngineEvent }

func (r *slowRuntime) Run(_ context.Context, _ runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	return r.events, nil
}

func TestAsync_DetachesFromParentCtx(t *testing.T) {
	mgr := tasks.NewMemory()
	rt := &slowRuntime{events: make(chan pkgtypes.EngineEvent, 4)}
	s := &Strategy{rt: rt, taskMgr: mgr, diskOut: diskout.NewFS(t.TempDir())}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "async", Bag: map[string]any{}}
	_ = mgr.Register(context.Background(), tasks.RegisterParams{
		TaskID: "t-1", AgentID: "a-1", Strategy: "async", StartedAt: time.Now(),
	})

	parentCtx, parentCancel := context.WithCancel(context.Background())
	_, err := s.Spawn(parentCtx, scheduler.SpawnParams{}, st)
	if err != nil {
		t.Fatal(err)
	}
	parentCancel() // 父立即取消

	// 后台应该继续；让 runtime 收尾
	rt.events <- pkgtypes.EngineEvent{Type: pkgtypes.EngineEventType("tail")}
	close(rt.events)

	final, err := mgr.Wait(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != tasks.TaskCompleted {
		t.Errorf("async should complete despite parent cancel; status %q", final.Status)
	}
}
