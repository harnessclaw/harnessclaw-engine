// Package runtime 声明 scheduler 看见的"agent 执行器"抽象。
// 实现侧在 internal/engine/loop/runtime/llm.go —— scheduler 包零 LLM import。
package runtime

import (
	"context"

	"harnessclaw-go/internal/engine/agent/definition"
	pkgtypes "harnessclaw-go/pkg/types"
)

type Runtime interface {
	// Run 启动 agent 执行循环。
	// 立即返回 <-chan EngineEvent；channel close = agent 终止。
	// 终止原因通过最后一条 event 的 Terminal 字段传递。
	// ctx 取消 → 实现必须在合理时间 close channel。
	Run(ctx context.Context, p RunParams) (<-chan pkgtypes.EngineEvent, error)
}

type RunParams struct {
	AgentID    pkgtypes.AgentID
	Definition definition.AgentDefinition
	Prompt     string
	Inputs     map[string]any
	InputPaths []string

	// ParentSession / RootSession 从 ctx 取（AgentContext middleware 注入）
	// —— 不在 RunParams 显式列，避免重复

	// Overrides 通过 scheduler.Overrides 透传；为避免循环 import，
	// 用独立 struct 复制相同字段。
	Overrides Overrides
}

type Overrides struct {
	Model      string
	MaxTurns   int
	Permission string
}
