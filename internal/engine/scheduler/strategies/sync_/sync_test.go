package sync_

import (
	"context"
	"errors"
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

func TestSync_CanHandle_AlwaysTrue(t *testing.T) {
	s := &Strategy{}
	if !s.CanHandle(scheduler.SpawnParams{}) {
		t.Error("sync 应作为兜底")
	}
}

func TestSync_Spawn_Completed(t *testing.T) {
	s := &Strategy{
		rt:      &fakeRuntime{events: []pkgtypes.EngineEvent{{Type: pkgtypes.EngineEventText, Text: "hi"}, {Type: pkgtypes.EngineEventDone}}},
		taskMgr: tasks.NewMemory(),
		diskOut: diskout.NewFS(t.TempDir()),
	}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "sync", Bag: map[string]any{}}

	res, err := s.Spawn(context.Background(), scheduler.SpawnParams{Prompt: "x"}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scheduler.StatusCompleted {
		t.Errorf("Status got %q", res.Status)
	}
	if _, ok := res.Outcome.(scheduler.SyncOutcome); !ok {
		t.Errorf("Outcome type %T", res.Outcome)
	}
}

type slowRuntime struct{ events chan pkgtypes.EngineEvent }

func (r *slowRuntime) Run(_ context.Context, _ runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	return r.events, nil
}

func TestSync_HandoffToBackgroundOnBgSignal(t *testing.T) {
	mgr := tasks.NewMemory()
	rt := &slowRuntime{events: make(chan pkgtypes.EngineEvent, 4)}
	rt.events <- pkgtypes.EngineEvent{Type: pkgtypes.EngineEventText, Text: "before-bg"}

	s := &Strategy{rt: rt, taskMgr: mgr, diskOut: diskout.NewFS(t.TempDir())}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "sync", Bag: map[string]any{}}

	// 触发 bg 信号
	go func() {
		time.Sleep(20 * time.Millisecond)
		mgr.RequestBackground("t-1")
	}()

	res, err := s.Spawn(context.Background(), scheduler.SpawnParams{Prompt: "x"}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scheduler.StatusAsyncLaunched {
		t.Errorf("切后台后 Status 应为 async_launched，got %q", res.Status)
	}
	if _, ok := res.Outcome.(scheduler.AsyncOutcome); !ok {
		t.Errorf("Outcome type %T", res.Outcome)
	}

	// 任务行应在切后台时懒注册
	info, ok := mgr.Get("t-1")
	if !ok {
		t.Fatal("懒注册失败")
	}
	if info.Strategy != "sync→async" {
		t.Errorf("Strategy got %q want sync→async", info.Strategy)
	}

	// 让 runtime 收尾
	rt.events <- pkgtypes.EngineEvent{Type: pkgtypes.EngineEventText, Text: "after-bg"}
	close(rt.events)
	_, _ = mgr.Wait(context.Background(), "t-1")
}

type erroringRuntime struct{ err error }

func (r *erroringRuntime) Run(_ context.Context, _ runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	return nil, r.err
}

func TestSync_PropagatesRuntimeError(t *testing.T) {
	sentinel := errors.New("boom")
	s := &Strategy{rt: &erroringRuntime{err: sentinel}, taskMgr: tasks.NewMemory(), diskOut: diskout.NewFS(t.TempDir())}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "sync", Bag: map[string]any{}}
	_, err := s.Spawn(context.Background(), scheduler.SpawnParams{}, st)
	if !errors.Is(err, sentinel) {
		t.Errorf("got err %v", err)
	}
}

// 权限请求帧绝不能被 fan-out 的非阻塞 drop 丢掉 —— sub-agent 的执行器
// 阻塞等待 root UI 应答。本测试用无缓冲父 channel + 延迟 reader 复现
// "父 channel 暂时不可写"：阻塞式透传必须最终送达。
func TestSync_PermissionRequestRelayNeverDropped(t *testing.T) {
	permEvt := pkgtypes.EngineEvent{
		Type:              pkgtypes.EngineEventPermissionRequest,
		PermissionRequest: &pkgtypes.PermissionRequest{RequestID: "perm_relay"},
	}
	s := &Strategy{
		rt:      &fakeRuntime{events: []pkgtypes.EngineEvent{permEvt, {Type: pkgtypes.EngineEventDone}}},
		taskMgr: tasks.NewMemory(),
		diskOut: diskout.NewFS(t.TempDir()),
	}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "sync", Bag: map[string]any{}}

	parent := make(chan pkgtypes.EngineEvent) // 无缓冲：非阻塞 send 必丢
	got := make(chan pkgtypes.EngineEvent, 1)
	go func() {
		time.Sleep(50 * time.Millisecond) // 模拟 reader 短暂繁忙
		got <- <-parent
	}()

	if _, err := s.Spawn(context.Background(), scheduler.SpawnParams{Prompt: "x", Events: parent}, st); err != nil {
		t.Fatal(err)
	}

	select {
	case evt := <-got:
		if evt.Type != pkgtypes.EngineEventPermissionRequest {
			t.Errorf("relayed event type = %q, want permission_request", evt.Type)
		}
		if evt.PermissionRequest == nil || evt.PermissionRequest.RequestID != "perm_relay" {
			t.Errorf("relayed PermissionRequest = %+v", evt.PermissionRequest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("permission_request was dropped by the relay")
	}
}

func TestSync_Subscribe_NotSupported(t *testing.T) {
	s := &Strategy{}
	_, err := s.Subscribe(context.Background(), pkgtypes.TaskID("t-x"))
	if !errors.Is(err, scheduler.ErrNotSubscribable) {
		t.Errorf("Subscribe should return ErrNotSubscribable, got %v", err)
	}
}
