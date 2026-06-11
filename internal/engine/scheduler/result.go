package scheduler

import (
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

// Result is the return type from Scheduler.Dispatch.
// It contains the final outcome (sync or async), usage stats, and timing info.
type Result struct {
	AgentID    pkgtypes.AgentID
	TaskID     pkgtypes.TaskID
	Strategy   string
	Status     Status
	Outcome    Outcome
	Usage      pkgtypes.Usage
	StartedAt  time.Time
	FinishedAt time.Time
}

// Status indicates the completion state of a dispatched agent.
type Status string

const (
	StatusCompleted     Status = "completed"
	StatusAsyncLaunched Status = "async_launched"
	StatusFailed        Status = "failed"
)

// Outcome is a sealed interface that represents either sync or async agent execution.
// The sealedOutcome() private method prevents external implementations.
// Callers consume via type switch:
//
//	switch o := r.Outcome.(type) {
//	case SyncOutcome:
//		return o.Content
//	case AsyncOutcome:
//		showProgress(o.OutputFile)
//	}
type Outcome interface {
	sealedOutcome()
}

// SyncOutcome represents the result of a synchronous (blocking) agent execution.
type SyncOutcome struct {
	Content   []pkgtypes.ContentBlock
	Terminal  pkgtypes.Terminal
	ToolCalls int

	// Deliverables 是 sub-agent 在执行过程中写盘的文件清单
	// （从 EngineEventDeliverable 事件累计）。
	Deliverables []pkgtypes.Deliverable

	// DeniedTools 是 sub-agent 尝试调用但被 permission 拒绝的工具名
	// （从 EngineEventSubAgentEnd 等含 DeniedTools 的事件累计）。
	DeniedTools []string

	// Artifacts 是 sub-agent 通过 ArtifactWrite 提交的跨 agent 制品引用
	// （从含 evt.Artifacts 的事件累计；通常是 tool_end / subagent_end）。
	Artifacts []pkgtypes.ArtifactRef
}

func (SyncOutcome) sealedOutcome() {}

// AsyncOutcome represents the result of an asynchronous agent dispatch.
// The agent continues running in the background; the caller can subscribe via Scheduler.Subscribe.
type AsyncOutcome struct {
	OutputFile string
	Tailable   bool
}

func (AsyncOutcome) sealedOutcome() {}
