package scheduler

import (
	"context"

	pkgtypes "harnessclaw-go/pkg/types"
)

// SpawnState 是 middleware 之间 + middleware/strategy 之间共享的可变状态。
// 显式可变结构体，不用 context.Value —— 协作的状态要"显眼"。
type SpawnState struct {
	AgentID  pkgtypes.AgentID
	TaskID   pkgtypes.TaskID
	Strategy string

	AbortCtx    context.Context
	AbortCancel context.CancelFunc

	Cleanups []CleanupFn
	Bag      map[string]any
}

type CleanupFn func(ctx context.Context)
