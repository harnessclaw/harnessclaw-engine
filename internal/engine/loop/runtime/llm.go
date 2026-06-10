// Package loopruntime is scheduler.Runtime 的 LLM 实现。
//
// 故意放在 internal/engine/loop/runtime 而非 scheduler/runtime/llm，
// 这样 scheduler 包严格零 LLM import —— 满足 spec 目标 4「LLM 解耦」。
//
// 内容 = runner.RunLeaf 的整体逻辑 + Option B 适配（loop.Out → 返回 channel）。
// 不重新实现 LLM 循环 —— loop.Run 仍是同一个 kernel；本文件只做
// SpawnParams → loop.Config 装配 + channel 桥。
package loopruntime

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/scheduler/middlewares"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/session"
	legacyagent "harnessclaw-go/internal/legacy/agent"
	common "harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	pkgtypes "harnessclaw-go/pkg/types"
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
// 把 RunParams 装配成 loop.Config：建子 session、建工具池、建 system prompt、
// 喂第一条 user 消息，然后在 goroutine 里跑 loop.Run，把 loop 写到 events
// channel 的事件透传给调用方（Option B 桥）。
//
// 终止：loop.Run 返回 → 关 channel。期间出错的话最后多发一帧 EngineEventError
// 携带 Terminal.Reason。
func (r *LLM) Run(ctx context.Context, p runtime.RunParams) (<-chan pkgtypes.EngineEvent, error) {
	ac, _ := middlewares.AgentCtxFrom(ctx)

	// 从 RunParams + ctx 合成 legacy SpawnConfig，供老 helper 复用。
	// SpawnConfig 是过渡期 carrier —— 后续 PR-4 删 SpawnConfig 后这里改成
	// 直接 inline 各 helper 的逻辑。
	cfg := &legacyagent.SpawnConfig{
		Prompt:          p.Prompt,
		AgentType:       p.Definition.AgentType,
		SubagentType:    p.Definition.Name, // observability label（spec 调和决策）
		Name:            p.Definition.Name,
		Model:           p.Overrides.Model,
		MaxTurns:        p.Overrides.MaxTurns,
		ParentSessionID: string(ac.SessionID),
		ParentAgentID:   string(ac.ParentAgentID),
		ParentStepID:    ac.ParentStepID,
		RootSessionID:   string(ac.RootSessionID),
		TaskID:          string(ac.TaskID),
		InputPaths:      p.InputPaths,
	}

	// 1. 建子 session（runner.RunLeaf:139）
	sess, err := common.BuildSubSession(r.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}

	// 2. 预创 task 目录（runner.RunLeaf:145）
	_ = common.EnsureTaskDir(cfg, r.Cfg.RootDir)

	// 3. 工具池（runner.RunLeaf:151-155）
	pool := common.BuildToolPool(r.Registry, p.Definition.AllowedTools, p.Definition.AgentType, false)

	// 4. Profile + system prompt（runner.RunLeaf:158-180）
	profile := resolveProfile(p.Definition.Profile)
	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           profile,
		Builder:           r.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      cfg.SubagentType,
		ContextWindow:     r.Cfg.ContextWindow,
		Registry:          r.Registry,
	})

	// 5. Seed 第一条 user 消息（runner.RunLeaf:198-203）
	sess.AddMessage(pkgtypes.Message{
		Role: pkgtypes.RoleUser,
		Content: []pkgtypes.ContentBlock{{
			Type: pkgtypes.ContentTypeText,
			Text: common.SeedPrompt(cfg, r.Cfg.RootDir),
		}},
	})

	// 6. MaxTurns 优先级：Overrides > Definition.MaxTurns > 10
	maxTurns := p.Overrides.MaxTurns
	if maxTurns <= 0 {
		maxTurns = p.Definition.MaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}

	// 7. 权限 checker（runner.RunLeaf:214-216）
	permChecker := common.BuildInheritedChecker(
		common.SessionApprovedTools(r.SessionMgr, cfg.ParentSessionID),
	)

	// 8. AgentScope（runner.RunLeaf:256）
	scope := common.BuildAgentScope(cfg, r.Cfg.RootDir, "leaf")

	// 9. Option B 桥：开 channel，goroutine 跑 loop，loop 的 sink 就是 channel
	events := make(chan pkgtypes.EngineEvent, 64)
	startedAt := time.Now()
	go func() {
		defer close(events)

		// ★ 关键：先发 subagent.start，wire 翻译层据此把后续 token 流归到
		// 新 sub-agent 名下而非 emma 主 agent。漏发会让 UI 把 L2/L3 输出
		// 错误归到父级 message card（hierarchy 错乱 + emma 回答 double）。
		common.EmitSubagentStart(events, common.StartEvent{
			AgentID:         string(p.AgentID),
			AgentName:       p.Definition.Name,
			AgentDesc:       cfg.Description,
			AgentTask:       p.Prompt,
			AgentType:       string(p.Definition.AgentType),
			SubagentType:    cfg.SubagentType,
			ParentAgentID:   cfg.ParentAgentID,
			ParentSessionID: cfg.ParentSessionID,
			ParentStepID:    cfg.ParentStepID,
		})

		res, rerr := loop.Run(ctx, &loop.Config{
			Session:             sess,
			SystemPrompt:        sysPrompt,
			Tools:               pool,
			Provider:            r.Provider,
			Compactor:           r.Compactor,
			Retryer:             r.Retryer,
			Logger:              r.Logger,
			MaxTurns:            maxTurns,
			MaxTokens:           r.Cfg.MaxTokens,
			ContextWindow:       r.Cfg.ContextWindow,
			ToolTimeout:         r.Cfg.ToolTimeout,
			LLMAPITimeout:       r.Cfg.LLMAPITimeout,
			LLMFirstByteTimeout: r.Cfg.LLMFirstByteTimeout,
			Out:                 events,
			AgentID:             string(p.AgentID),
			PermChecker:         permChecker,
			AgentScope:          scope,
			OnTurnComplete:      common.StopOnEndTurn(),
		})
		durationMs := time.Since(startedAt).Milliseconds()
		if rerr != nil {
			term := &pkgtypes.Terminal{
				Reason:  pkgtypes.TerminalModelError,
				Message: rerr.Error(),
			}
			events <- pkgtypes.EngineEvent{Type: pkgtypes.EngineEventError, Terminal: term}
			// 失败路径也要发 subagent.end，否则 wire 翻译层永远不关 agent 卡
			common.EmitSubagentEnd(events, common.EndEvent{
				AgentID:         string(p.AgentID),
				AgentName:       p.Definition.Name,
				AgentStatus:     "failed",
				SubagentType:    cfg.SubagentType,
				DurationMs:      durationMs,
				Terminal:        term,
				ParentAgentID:   cfg.ParentAgentID,
				ParentSessionID: cfg.ParentSessionID,
			})
			return
		}
		// 把 loop.Result 的 Terminal / Usage 编码成一帧 EngineEventDone，
		// SyncStrategy.accumulate 会收到并填进 SyncOutcome。
		// loop.Run 自身不发 Done event —— Terminal 信息只在 *Result 上，
		// 不补这帧的话 caller 收不到终止原因。
		termEvt := pkgtypes.EngineEvent{Type: pkgtypes.EngineEventDone, Terminal: &res.Terminal}
		usage := res.Usage
		termEvt.Usage = &usage
		events <- termEvt

		// 正常路径 subagent.end —— 关闭 agent 卡，归属回父 agent
		common.EmitSubagentEnd(events, common.EndEvent{
			AgentID:         string(p.AgentID),
			AgentName:       p.Definition.Name,
			AgentStatus:     statusFromTerminal(res.Terminal),
			SubagentType:    cfg.SubagentType,
			DurationMs:      durationMs,
			Usage:           &usage,
			Terminal:        &res.Terminal,
			ParentAgentID:   cfg.ParentAgentID,
			ParentSessionID: cfg.ParentSessionID,
		})
	}()
	return events, nil
}

// statusFromTerminal 把 Terminal.Reason 映射到 subagent.end 的 agent_status 字符串。
func statusFromTerminal(t pkgtypes.Terminal) string {
	switch t.Reason {
	case pkgtypes.TerminalCompleted:
		return "completed"
	case pkgtypes.TerminalMaxTurns:
		return "max_turns"
	case pkgtypes.TerminalAbortedStreaming, pkgtypes.TerminalAbortedTools:
		return "aborted"
	default:
		return "failed"
	}
}

// resolveProfile 把 Definition.Profile 字符串解析成 *prompt.AgentProfile。
// 空 → WorkerProfile（与 runner.RunLeaf:302-310 一致）。
func resolveProfile(name string) *prompt.AgentProfile {
	if name == "" {
		return prompt.WorkerProfile
	}
	if p, ok := prompt.GetBuiltInProfiles()[name]; ok && p != nil {
		return p
	}
	return prompt.WorkerProfile
}

// 编译期接口实现检查
var _ runtime.Runtime = (*LLM)(nil)
