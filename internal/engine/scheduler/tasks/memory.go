package tasks

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

type Memory struct {
	mu    sync.RWMutex
	tasks map[pkgtypes.TaskID]*taskEntry
}

type taskEntry struct {
	info  TaskInfo
	done  chan struct{} // close 时 = terminal
	bgSig chan struct{} // close 时 = 用户请求背景化
}

func NewMemory() *Memory {
	return &Memory{tasks: make(map[pkgtypes.TaskID]*taskEntry)}
}

func (m *Memory) Register(ctx context.Context, p RegisterParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, exists := m.tasks[p.TaskID]; exists && e.info.Status != "" {
		return fmt.Errorf("tasks: %s already registered", p.TaskID)
	}
	// 兼容前台 task 已通过 RegisterForeground 占位的情况
	existing := m.tasks[p.TaskID]
	entry := &taskEntry{
		info: TaskInfo{
			TaskID:         p.TaskID,
			AgentID:        p.AgentID,
			Name:           p.Name,
			Description:    p.Description,
			SubagentType:   p.SubagentType,
			Strategy:       p.Strategy,
			Status:         TaskRunning,
			StartedAt:      p.StartedAt,
			LastActivityAt: p.StartedAt,
		},
		done: make(chan struct{}),
	}
	if existing != nil && existing.bgSig != nil {
		entry.bgSig = existing.bgSig
	}
	m.tasks[p.TaskID] = entry
	return nil
}

func (m *Memory) Tick(_ context.Context, taskID pkgtypes.TaskID, _ pkgtypes.EngineEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("tasks: %s not found", taskID)
	}
	e.info.EventCount++
	e.info.LastActivityAt = time.Now()
	return nil
}

func (m *Memory) Complete(_ context.Context, taskID pkgtypes.TaskID) error {
	return m.terminate(taskID, TaskCompleted, "")
}

func (m *Memory) Fail(_ context.Context, taskID pkgtypes.TaskID, err error) error {
	return m.terminate(taskID, TaskFailed, err.Error())
}

func (m *Memory) Cancel(_ context.Context, taskID pkgtypes.TaskID) error {
	return m.terminate(taskID, TaskCancelled, "cancelled")
}

func (m *Memory) terminate(taskID pkgtypes.TaskID, status TaskStatus, errStr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("tasks: %s not found", taskID)
	}
	if e.info.Status != TaskRunning {
		return nil // 幂等
	}
	e.info.Status = status
	e.info.LastError = errStr
	e.info.LastActivityAt = time.Now()
	if e.done != nil {
		close(e.done)
	}
	return nil
}

func (m *Memory) MarkLaunched(_ context.Context, taskID pkgtypes.TaskID, info LaunchInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("tasks: %s not found", taskID)
	}
	if info.Strategy != "" {
		e.info.Strategy = info.Strategy
	}
	return nil
}

func (m *Memory) MarkLaunchFailed(_ context.Context, taskID pkgtypes.TaskID, err error) error {
	return m.terminate(taskID, TaskFailed, err.Error())
}

func (m *Memory) RegisterForeground(_ context.Context, taskID pkgtypes.TaskID, _ pkgtypes.AgentID) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	// sync 路径不预 Register，所以前台 task 可能还不在 map 里 —— 单独存
	if e, ok := m.tasks[taskID]; ok {
		if e.bgSig == nil {
			e.bgSig = make(chan struct{})
		}
		return e.bgSig
	}
	bg := make(chan struct{})
	m.tasks[taskID] = &taskEntry{bgSig: bg, done: make(chan struct{})}
	return bg
}

func (m *Memory) UnregisterForeground(taskID pkgtypes.TaskID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[taskID]
	if !ok {
		return
	}
	// 仅清理占位 entry（Status 为空表示从未 Register 过）
	if e.info.Status == "" {
		delete(m.tasks, taskID)
	}
}

func (m *Memory) RequestBackground(taskID pkgtypes.TaskID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[taskID]
	if !ok || e.bgSig == nil {
		return
	}
	select {
	case <-e.bgSig: // 已 close
	default:
		close(e.bgSig)
	}
}

func (m *Memory) Wait(ctx context.Context, taskID pkgtypes.TaskID) (TaskInfo, error) {
	m.mu.RLock()
	e, ok := m.tasks[taskID]
	m.mu.RUnlock()
	if !ok {
		return TaskInfo{}, fmt.Errorf("tasks: %s not found", taskID)
	}
	select {
	case <-e.done:
		m.mu.RLock()
		defer m.mu.RUnlock()
		return e.info, nil
	case <-ctx.Done():
		return TaskInfo{}, ctx.Err()
	}
}

func (m *Memory) Get(taskID pkgtypes.TaskID) (TaskInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.tasks[taskID]
	if !ok || e.info.Status == "" {
		return TaskInfo{}, false
	}
	return e.info, true
}

func (m *Memory) List() []TaskInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TaskInfo, 0, len(m.tasks))
	for _, e := range m.tasks {
		if e.info.Status != "" {
			out = append(out, e.info)
		}
	}
	return out
}

var _ Manager = (*Memory)(nil)
