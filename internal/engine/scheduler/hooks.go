package scheduler

import (
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

const (
	TopicSpawnStarted   = "scheduler.spawn.started"
	TopicSpawnCompleted = "scheduler.spawn.completed"
	TopicSpawnFailed    = "scheduler.spawn.failed"
)

type SpawnStartedPayload struct {
	AgentID      pkgtypes.AgentID
	TaskID       pkgtypes.TaskID
	Strategy     string
	SubagentType string
	InvokedBy    Invoker
	StartedAt    time.Time
}

type SpawnFinishedPayload struct {
	AgentID    pkgtypes.AgentID
	TaskID     pkgtypes.TaskID
	Strategy   string
	Status     Status
	DurationMs int64
	Err        string // 空串 = 成功
}
