// Package scheduler is the unified agent dispatch entry point.
// Callers depend only on the Scheduler interface — never on Strategy / Middleware impls.
//
// Design intent: see docs/superpowers/specs/2026-06-09-scheduler-module-design.md
package scheduler

import (
	"context"

	pkgtypes "harnessclaw-go/pkg/types"
)

// Scheduler 是调用方看见的唯一接口。
// 设计核心原则："对已 spawn 的 taskID 操作"全部归 tasks.Manager / persistence；
// Scheduler 只负责 ① Dispatch（从无到有 spawn）和 ② Subscribe（取实时流）。
type Scheduler interface {
	// Dispatch 从无到有 spawn 一个 agent。
	// sync 阻塞返完整 SyncOutcome；async 立刻返带 TaskID 的 AsyncOutcome。
	// 任何模式都从这里进。
	Dispatch(ctx context.Context, params SpawnParams) (Result, error)

	// Subscribe 取一个已 spawn 的 task 的实时事件流。
	// 仅对 async 系列 strategy（async / sync→async）可用；
	// 其他情况返 ErrNotSubscribable。
	Subscribe(ctx context.Context, taskID pkgtypes.TaskID) (<-chan pkgtypes.EngineEvent, error)
}
