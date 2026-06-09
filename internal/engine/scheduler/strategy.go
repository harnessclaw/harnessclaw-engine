package scheduler

import (
	"context"

	pkgtypes "harnessclaw-go/pkg/types"
)

type Strategy interface {
	Name() string
	CanHandle(p SpawnParams) bool
	Spawn(ctx context.Context, p SpawnParams, st *SpawnState) (Result, error)

	// Subscribe 返回该 task 的实时事件流。
	// 不支持的 strategy 返 ErrNotSubscribable。
	// Dispatcher.Subscribe 按 taskInfo.Strategy 路由到这里。
	Subscribe(ctx context.Context, taskID pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error)
}
