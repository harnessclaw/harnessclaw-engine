// Package middlewares 是 scheduler 内置的横切 middleware 集合。
// 顺序写死在 Dispatcher 构造里：Identity → AgentContext → TaskRegister → Analytics。
package middlewares

import (
	"context"

	"github.com/google/uuid"

	"harnessclaw-go/internal/engine/scheduler"
	pkgtypes "harnessclaw-go/pkg/types"
)

// Identity 分配 agentID / taskID。
// 必须最先跑 —— 所有后续 MW 都依赖 state.AgentID/TaskID。
type Identity struct{}

func (Identity) Name() string { return "identity" }

func (Identity) Before(ctx context.Context, _ scheduler.SpawnParams, st *scheduler.SpawnState) (context.Context, error) {
	st.AgentID = pkgtypes.AgentID("a-" + shortUUID())
	st.TaskID = pkgtypes.TaskID("t-" + shortUUID())
	return ctx, nil
}

func (Identity) After(context.Context, scheduler.SpawnParams, *scheduler.SpawnState, scheduler.Result, error) {
}

func shortUUID() string {
	s := uuid.NewString()
	return s[:12] // 12 hex chars 足够无冲突
}
