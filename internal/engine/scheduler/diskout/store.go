// Package diskout 提供 task 事件流的磁盘存储与尾随。
package diskout

import (
	"context"
	"io"

	pkgtypes "harnessclaw-go/pkg/types"
)

type Store interface {
	Open(taskID pkgtypes.TaskID) (Writer, error)
	Path(taskID pkgtypes.TaskID) string
	Reader(taskID pkgtypes.TaskID) (io.ReadCloser, error)

	// Tail 实时尾随：每行 NDJSON 解码后写入 out；
	// ctx 取消 → 关闭 out 返回。
	// AsyncStrategy.Subscribe 用它把磁盘流转 channel。
	Tail(ctx context.Context, taskID pkgtypes.TaskID, reader io.Reader, out chan<- pkgtypes.EngineEvent)
}

type Writer interface {
	Append(evt pkgtypes.EngineEvent) error
	AppendBlock(blk pkgtypes.ContentBlock) error
	Close() error
}
