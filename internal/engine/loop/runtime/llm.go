// Package loopruntime is scheduler.Runtime 的 LLM 实现。
//
// 故意放在 internal/engine/loop/runtime 而非 scheduler/runtime/llm，
// 这样 scheduler 包严格零 LLM import —— 满足 spec 目标 4「LLM 解耦」。
//
// PR-1 状态：本文件是骨架（stub）。真正的 LLM 装配逻辑（建 sub-session
// + 建 ToolPool + 建 SystemPrompt + 调 loop.Run）在 PR-3 emma 切换时
// port 自 internal/engine/agent/runAgent/runner/runner.go:139-280 (RunLeaf)。
//
// 留 stub 的原因：
//   - PR-1 没有 E2E smoke 测，无法验证 port 正确性
//   - PR-3 emma 切换时会同步加 smoke 测，那时是 port 的合适时机
//   - 当前 stub 足够让 scheduler 包 + 调用方 compile-time wiring 通过
package loopruntime

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	pkgtypes "harnessclaw-go/pkg/types"
	"time"
)

// LLM 是 scheduler.Runtime 的实际实现。
// 持有 LLM 栈的所有依赖；scheduler 包通过 runtime.Runtime interface 间接调用。
type LLM struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger
	Cfg           Config
}

// Config 是 LLM Runtime 运行时配置。所有字段从 emma config 透传。
type Config struct {
	MaxTokens           int
	ContextWindow       int
	ToolTimeout         time.Duration
	LLMAPITimeout       time.Duration
	LLMFirstByteTimeout time.Duration
	RootDir             string
}

// LLMArgs 是 NewLLM 的构造参数。
type LLMArgs struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger
	Cfg           Config
}

// NewLLM 构造一个 LLM Runtime。
func NewLLM(a LLMArgs) *LLM {
	return &LLM{
		Provider:      a.Provider,
		Registry:      a.Registry,
		SessionMgr:    a.SessionMgr,
		Compactor:     a.Compactor,
		Retryer:       a.Retryer,
		PromptBuilder: a.PromptBuilder,
		Logger:        a.Logger,
		Cfg:           a.Cfg,
	}
}

// Run 启动 agent 执行循环。
//
// PR-1 stub: 立刻返回一个 close 掉的 channel，伴随一条 EngineEventError，
// 把 Terminal.Reason 标记为 "not_implemented"。
// PR-3 将这里 port 自 runner.RunLeaf —— 见包注释。
func (r *LLM) Run(ctx context.Context, p runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	events := make(chan pkgtypes.EngineEvent, 1)
	go func() {
		defer close(events)
		events <- pkgtypes.EngineEvent{
			Type: pkgtypes.EngineEventError,
			Terminal: &pkgtypes.Terminal{
				Reason:  pkgtypes.TerminalModelError,
				Message: "loopruntime.LLM.Run: stub — PR-3 will port runner.RunLeaf body here",
			},
		}
	}()
	return events, nil
}

// 编译期接口实现检查
var _ runtime.Runtime = (*LLM)(nil)
