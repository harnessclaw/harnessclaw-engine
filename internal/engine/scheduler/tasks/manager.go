// Package tasks holds the task lifecycle manager used by scheduler middleware
// and exposed to external callers for taskID-keyed operations (Wait/Cancel/Get/List).
package tasks

import (
	"context"
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

type Manager interface {
	// —— scheduler 内部用 ——
	Register(ctx context.Context, p RegisterParams) error
	Tick(ctx context.Context, taskID pkgtypes.TaskID, evt pkgtypes.EngineEvent) error
	Complete(ctx context.Context, taskID pkgtypes.TaskID) error
	Fail(ctx context.Context, taskID pkgtypes.TaskID, err error) error
	MarkLaunched(ctx context.Context, taskID pkgtypes.TaskID, info LaunchInfo) error
	MarkLaunchFailed(ctx context.Context, taskID pkgtypes.TaskID, err error) error

	// —— 前台/后台切换 ——
	RegisterForeground(ctx context.Context, taskID pkgtypes.TaskID, agentID pkgtypes.AgentID) <-chan struct{}
	UnregisterForeground(taskID pkgtypes.TaskID)
	RequestBackground(taskID pkgtypes.TaskID)

	// —— 外部操作 API（按 taskID 操作，不走 scheduler）——
	Cancel(ctx context.Context, taskID pkgtypes.TaskID) error
	Wait(ctx context.Context, taskID pkgtypes.TaskID) (TaskInfo, error)
	Get(taskID pkgtypes.TaskID) (TaskInfo, bool)
	List() []TaskInfo
}

type RegisterParams struct {
	TaskID       pkgtypes.TaskID
	AgentID      pkgtypes.AgentID
	Name         string
	Description  string
	SubagentType string
	Strategy     string
	StartedAt    time.Time
	InvokedBy    InvokerSnapshot // 避免 import scheduler 形成循环 —— copy 必要字段
}

type InvokerSnapshot struct {
	Kind     string
	Source   string
	ParentID pkgtypes.AgentID
}

type LaunchInfo struct {
	OutputFile string
	Strategy   string
}

type TaskInfo struct {
	TaskID         pkgtypes.TaskID
	AgentID        pkgtypes.AgentID
	Name           string
	Description    string
	SubagentType   string
	Strategy       string
	Status         TaskStatus
	StartedAt      time.Time
	LastActivityAt time.Time
	EventCount     int
	LastError      string
}

type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)
