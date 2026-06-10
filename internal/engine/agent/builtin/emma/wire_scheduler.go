package emma

import (
	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/compact"
	loopruntime "harnessclaw-go/internal/engine/loop/runtime"
	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/diskout"
	"harnessclaw-go/internal/engine/scheduler/emit"
	"harnessclaw-go/internal/engine/scheduler/middlewares"
	asyncstrat "harnessclaw-go/internal/engine/scheduler/strategies/async"
	syncstrat "harnessclaw-go/internal/engine/scheduler/strategies/sync_"
	"harnessclaw-go/internal/engine/scheduler/tasks"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
)

// wiredDeps 汇总 wireScheduler 需要的所有依赖；从 emma.New 内已构造好的
// 字段一处拼起来传给 wireScheduler，避免函数签名爆炸。
type wiredDeps struct {
	Provider      provider.Provider
	ToolRegistry  *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger
	Cfg           Config
	WorkspaceRoot string
}

// wireScheduler 装配 scheduler.Scheduler，注入：
//   - Runtime: loopruntime.LLM（持有完整 LLM 栈）
//   - TaskMgr: in-memory tasks.Memory
//   - DiskOutput: WORKSPACE/tasks 下 NDJSON 文件
//   - Bus: in-memory emit pub/sub
//
// Middleware 链：middlewares.DefaultChain(deps) —— Identity → AgentContext →
// TaskRegister → Analytics（顺序固定）。
//
// Strategy 注册顺序 = 优先级：
//   1) async  —— 命中 Hints.Background
//   2) sync   —— 兜底
func wireScheduler(wd wiredDeps) scheduler.Scheduler {
	rt := loopruntime.NewLLM(loopruntime.LLMArgs{
		Provider:      wd.Provider,
		Registry:      wd.ToolRegistry,
		SessionMgr:    wd.SessionMgr,
		Compactor:     wd.Compactor,
		Retryer:       wd.Retryer,
		PromptBuilder: wd.PromptBuilder,
		Logger:        wd.Logger,
		Cfg: loopruntime.Config{
			MaxTokens:           wd.Cfg.MaxTokens,
			ContextWindow:       wd.Cfg.ContextWindow,
			ToolTimeout:         wd.Cfg.ToolTimeout,
			LLMAPITimeout:       wd.Cfg.LLMAPITimeout,
			LLMFirstByteTimeout: wd.Cfg.LLMFirstByteTimeout,
			RootDir:             wd.WorkspaceRoot,
		},
	})

	deps := scheduler.Deps{
		Runtime:    rt,
		TaskMgr:    tasks.NewMemory(),
		DiskOutput: diskout.NewFS(wd.WorkspaceRoot + "/tasks"),
		Bus:        emit.NewMemory(),
		Log:        wd.Logger,
	}

	return scheduler.NewDispatcher(
		deps,
		middlewares.DefaultChain(deps),
		asyncstrat.New(deps),
		syncstrat.New(deps),
	)
}
