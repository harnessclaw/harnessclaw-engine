package diskout

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

type FS struct {
	rootDir string
}

func NewFS(rootDir string) *FS {
	_ = os.MkdirAll(rootDir, 0o755)
	return &FS{rootDir: rootDir}
}

func (s *FS) Path(taskID pkgtypes.TaskID) string {
	return filepath.Join(s.rootDir, string(taskID)+".jsonl")
}

func (s *FS) Open(taskID pkgtypes.TaskID) (Writer, error) {
	f, err := os.OpenFile(s.Path(taskID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("diskout: open %s: %w", taskID, err)
	}
	return &fsWriter{f: f}, nil
}

func (s *FS) Reader(taskID pkgtypes.TaskID) (io.ReadCloser, error) {
	f, err := os.Open(s.Path(taskID))
	if err != nil {
		return nil, fmt.Errorf("diskout: read %s: %w", taskID, err)
	}
	return f, nil
}

func (s *FS) Tail(ctx context.Context, _ pkgtypes.TaskID, reader io.Reader, out chan<- pkgtypes.EngineEvent) {
	defer close(out)
	br := bufio.NewReader(reader)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line, err := br.ReadBytes('\n')
		if err == io.EOF || (err == nil && len(line) == 0) {
			// 轮询：100ms 后再试
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		var evt pkgtypes.EngineEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		select {
		case out <- evt:
		case <-ctx.Done():
			return
		}
	}
}

type fsWriter struct {
	f  *os.File
	mu sync.Mutex
}

func (w *fsWriter) Append(evt pkgtypes.EngineEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.f.Write(b)
	return err
}

// AppendBlock 把 ContentBlock 编码成 EngineEventText 帧写盘。
// 非文本 block (tool_use / image) 在 sync→async backfill 中损失细节 —— 这是可接受的折中。
func (w *fsWriter) AppendBlock(blk pkgtypes.ContentBlock) error {
	return w.Append(pkgtypes.EngineEvent{Type: pkgtypes.EngineEventText, Text: blk.Text})
}

func (w *fsWriter) Close() error {
	return w.f.Close()
}

// 编译期接口实现检查
var _ Store = (*FS)(nil)
var _ Writer = (*fsWriter)(nil)
