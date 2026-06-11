package scheduler

import (
	"time"

	"harnessclaw-go/internal/engine/agent/definition"
	pkgtypes "harnessclaw-go/pkg/types"
)

// SpawnParams 是 Dispatch 的不可变输入。
type SpawnParams struct {
	Definition  definition.AgentDefinition
	Prompt      string
	Description string
	Name        string

	Hints     Hints
	Parent    *ParentRef // nil 表示顶层（bootstrap / 无父）
	InvokedBy Invoker

	Inputs     map[string]any
	InputPaths []string

	Events chan<- pkgtypes.EngineEvent // 父事件 sink，nil 表示不订阅

	Overrides Overrides
}

type Overrides struct {
	Model      string
	MaxTurns   int
	Permission string
	Timeout    time.Duration
}

type Hints struct {
	Background bool   // → AsyncStrategy
	Isolation  string // 首期空；将来 worktree / remote 自己解析

	// Force 强制路由到指定 strategy 名，绕过 CanHandle 链。
	// 未注册的策略名 → ErrUnknownStrategy。
	Force string
}

type Invoker struct {
	Kind     InvokerKind
	Source   string // tool_use_id / command_name / step_id
	ParentID pkgtypes.AgentID
}

type InvokerKind string

const (
	InvokerLLM         InvokerKind = "llm"
	InvokerUserCommand InvokerKind = "user_command"
	InvokerCoordinator InvokerKind = "coordinator"
	InvokerBootstrap   InvokerKind = "bootstrap"
	InvokerMention     InvokerKind = "mention"
)

type ParentRef struct {
	AgentID       pkgtypes.AgentID
	SessionID     pkgtypes.SessionID
	StepID        string
	RootSessionID pkgtypes.SessionID
	Cwd           string
	PriorMessages []pkgtypes.Message // resume/fork 时填，首期可空
}
