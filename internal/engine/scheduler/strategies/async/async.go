package async

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/diskout"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/scheduler/tasks"
	pkgtypes "harnessclaw-go/pkg/types"
)

type Strategy struct {
	rt      runtime.Runtime
	taskMgr tasks.Manager
	diskOut diskout.Store
}

func New(deps scheduler.Deps) *Strategy {
	return &Strategy{rt: deps.Runtime, taskMgr: deps.TaskMgr, diskOut: deps.DiskOutput}
}

func (s *Strategy) Name() string { return "async" }

func (s *Strategy) CanHandle(p scheduler.SpawnParams) bool {
	return p.Hints.Background
}

func (s *Strategy) Spawn(ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState) (scheduler.Result, error) {
	// detach context —— 父 ctx cancel 不杀后台 agent；保留 ctx.Value
	bgCtx, bgCancel := context.WithCancel(context.WithoutCancel(ctx))
	st.AbortCtx, st.AbortCancel = bgCtx, bgCancel

	writer, err := s.diskOut.Open(st.TaskID)
	if err != nil {
		bgCancel()
		return scheduler.Result{}, err
	}

	go func() {
		defer writer.Close()
		defer runCleanups(bgCtx, st)
		defer bgCancel()

		msgs, runErr := s.rt.Run(bgCtx, runtime.RunParams{
			AgentID:    st.AgentID,
			Definition: p.Definition,
			Prompt:     p.Prompt,
			Inputs:     p.Inputs,
			InputPaths: p.InputPaths,
			Overrides: runtime.Overrides{
				Model:      p.Overrides.Model,
				MaxTurns:   p.Overrides.MaxTurns,
				Permission: p.Overrides.Permission,
			},
		})
		if runErr != nil {
			_ = s.taskMgr.Fail(bgCtx, st.TaskID, runErr)
			return
		}
		for evt := range msgs {
			_ = writer.Append(evt)
			_ = s.taskMgr.Tick(bgCtx, st.TaskID, evt)
			if p.Events != nil {
				select {
				case p.Events <- evt:
				default: // 慢父订阅者：丢字幕，磁盘流是 source of truth
				}
			}
		}
		_ = s.taskMgr.Complete(bgCtx, st.TaskID)
	}()

	return scheduler.Result{
		Status:     scheduler.StatusAsyncLaunched,
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
		Outcome: scheduler.AsyncOutcome{
			OutputFile: s.diskOut.Path(st.TaskID),
			Tailable:   true,
		},
	}, nil
}

func (s *Strategy) Subscribe(ctx context.Context, taskID pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error) {
	reader, err := s.diskOut.Reader(taskID)
	if err != nil {
		return nil, err
	}

	out := make(chan pkgtypes.EngineEvent, 64)
	go func() {
		defer reader.Close()
		s.diskOut.Tail(ctx, taskID, reader, out)
	}()
	return out, nil
}

func runCleanups(ctx context.Context, st *scheduler.SpawnState) {
	for i := len(st.Cleanups) - 1; i >= 0; i-- {
		st.Cleanups[i](ctx)
	}
	st.Cleanups = nil
}

// 编译期接口实现检查
var _ scheduler.Strategy = (*Strategy)(nil)
