package middlewares

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/tasks"
)

type TaskRegister struct {
	Mgr tasks.Manager
}

func (TaskRegister) Name() string { return "task_register" }

func (m TaskRegister) Before(ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState) (context.Context, error) {
	if !needsTaskRow(st.Strategy) {
		return ctx, nil
	}
	return ctx, m.Mgr.Register(ctx, tasks.RegisterParams{
		TaskID:       st.TaskID,
		AgentID:      st.AgentID,
		Name:         p.Name,
		Description:  p.Description,
		SubagentType: p.Definition.Name, // 用 Definition.Name 作为 observability 标签
		Strategy:     st.Strategy,
		StartedAt:    time.Now(),
		InvokedBy: tasks.InvokerSnapshot{
			Kind:     string(p.InvokedBy.Kind),
			Source:   p.InvokedBy.Source,
			ParentID: p.InvokedBy.ParentID,
		},
	})
}

func (m TaskRegister) After(ctx context.Context, _ scheduler.SpawnParams, st *scheduler.SpawnState, r scheduler.Result, err error) {
	if !needsTaskRow(st.Strategy) {
		return
	}
	if err != nil {
		_ = m.Mgr.MarkLaunchFailed(ctx, st.TaskID, err)
		return
	}
	if r.Status == scheduler.StatusAsyncLaunched {
		_ = m.Mgr.MarkLaunched(ctx, st.TaskID, tasks.LaunchInfo{Strategy: r.Strategy})
	}
}

// needsTaskRow 决定哪些 strategy 需要预注册 task 行。
// Sync 默认不预注册（sync 跑完就结束，没有"运行中任务"语义）；
// Ctrl+B 切后台时由 SyncStrategy 内部懒注册（不走 middleware）。
func needsTaskRow(strategyName string) bool {
	return strategyName == "async"
}
