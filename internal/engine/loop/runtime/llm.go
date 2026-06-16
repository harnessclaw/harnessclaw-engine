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
	"fmt"
	"time"

	"go.uber.org/zap"

	browseragentdef "harnessclaw-go/internal/engine/agent/builtin/browser_agent"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/scheduler/middlewares"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skills"
	"harnessclaw-go/internal/skills/tracker"
	"harnessclaw-go/internal/tools"
	browsertools "harnessclaw-go/internal/tools/builtin/browser"
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
	SkillReader   *skill.Reader // nil → freelancer 的 skill 自管理子系统未启用
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
	SkillReader   *skill.Reader
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
		SkillReader:   a.SkillReader,
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
	cfg := &common.SpawnConfig{
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

	// ★ 4.5 Skill hydration —— freelancer 专属
	// 装 SkillTracker：state machine 簿记 active/unloaded skill + 强制 budget。
	// candidate_skills（从 dispatch 工具的 Inputs 里来）会被 Preload 进 tracker
	// 并把 <loaded-skills> XML 块前置拼到 sysPrompt 上。tracker 注入 ctx 后
	// load_skill / unload_skill / list_loaded_skills 这 3 个工具就能从 ctx
	// 取到状态机进行操作。
	// 非 freelancer（emma / browser / explore 等）不进这条路径，tracker == nil，
	// 工具调用方报「skill tracker not available」是预期行为。
	if p.Definition.Name == "freelancer" && r.SkillReader != nil {
		candidates := parseCandidateSkills(p.Inputs)
		tr, skillBlock, hErr := hydrateSkills(r.SkillReader, candidates)
		if hErr != nil {
			return nil, fmt.Errorf("freelancer skill hydration: %w", hErr)
		}
		ctx = tool.WithSkillTrackerValue(ctx, tr)
		if skillBlock != "" {
			sysPrompt = skillBlock + "\n\n" + sysPrompt
		}
	}

	var browserBinding *browsertools.TaskBinding
	if p.Definition.Name == browseragentdef.AgentName {
		browserBinding = browsertools.NewTaskBinding(cfg.TaskID)
		ctx = browsertools.WithTaskBinding(ctx, browserBinding)
	}

	// 5. Seed 第一条 user 消息（runner.RunLeaf:198-203）
	sess.AddMessage(pkgtypes.Message{
		Role: pkgtypes.RoleUser,
		Content: []pkgtypes.ContentBlock{{
			Type: pkgtypes.ContentTypeText,
			Text: common.SeedPrompt(cfg, r.Cfg.RootDir),
		}},
	})

	// 6. MaxTurns 优先级：Overrides > Definition.MaxTurns > 30
	// 默认 30 是 L3 sub-agent（freelancer/explore/plan 等）的常见预算。
	// 10 是经验上太低的值 —— 写一篇 1000 字散文需要 search → load skill → think
	// → write → verify 的 5-8 个 step，10 turns 必触 max_turns。
	// 调用方需要严格限流时通过 Overrides 显式覆写（如 browseragent 的 maxSteps）。
	maxTurns := p.Overrides.MaxTurns
	if maxTurns <= 0 {
		maxTurns = p.Definition.MaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 30
	}

	// 7. 权限 checker（runner.RunLeaf:214-216）
	permChecker := common.BuildInheritedChecker(
		common.SessionApprovedTools(r.SessionMgr, cfg.ParentSessionID),
	)

	// 7.5 权限 Ask 冒泡：sub-agent 自己没有 UI，未授权的写工具触发 Ask 时，
	// 把 PermissionRequest 注册到 ROOT session 的 Awaits（websocket 的
	// SubmitPermissionResult 解析在 root session 上），事件经 dispatch relay
	// 透传到 root UI 弹窗。RootSessionID 为空（直接 spawn）时退回 parent。
	approvalSessID := cfg.RootSessionID
	if approvalSessID == "" {
		approvalSessID = cfg.ParentSessionID
	}
	approvalFn := common.BuildSubAgentApprovalFn(r.SessionMgr, approvalSessID, r.Logger)

	// 8. AgentScope（runner.RunLeaf:256）
	scope := common.BuildAgentScope(cfg, r.Cfg.RootDir, "leaf")

	// ★ 9. 把本 sub-agent 的 sessionstats 注入 ctx ——
	// 下游工具（agenttool/browseragent 等）通过 sessionstats.*FromCtx 取
	// SessionID / RootSessionID / ImmediateParentSessionID 作为 sub-agent 归属。
	// runner.RunLeaf:194 原本就有这一步，port 时漏了 —— 导致 L2 调 freelance 时
	// agenttool 读到的还是 emma 的 ctx，sub-agent session 归属全错乱（最终症状是
	// 工作目录注入失败、freelancer 把文件写到 repo 根目录而不是 task_dir）。
	ctx = common.WithSubAgentStats(ctx, sess.ID, sess.ID, cfg.ParentSessionID, cfg.RootSessionID)

	// 10. Option B 桥：开 channel，goroutine 跑 loop，loop 的 sink 就是 channel
	events := make(chan pkgtypes.EngineEvent, 64)
	startedAt := time.Now()
	baseOnTurnComplete := common.StopOnEndTurn()
	onTurnComplete := baseOnTurnComplete
	var hooks loop.Hooks
	if browserBinding != nil {
		hooks.OnToolResult = func(_ int, call pkgtypes.ToolCall, result pkgtypes.ToolResult) {
			browsertools.UpdateTaskBindingFromToolResult(call.Name, result, browserBinding)
		}
		onTurnComplete = func(snap loop.TurnSnapshot) loop.Decision {
			browsertools.UpdateTaskBindingFromResults(snap.AssistantMsg, snap.ToolResults, browserBinding)
			return baseOnTurnComplete(snap)
		}
	}
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
			ApprovalFn:          approvalFn,
			AgentScope:          scope,
			Hooks:               hooks,
			OnTurnComplete:      onTurnComplete,
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

// freelancer skill budget —— 经验值 3，与历史 hydrateFreelancer 一致。
// L2 candidate + L3 runtime load_skill 共享这个上限。
const freelancerSkillBudget = 3

// hydrateSkills 构造 freelancer 启动时的 SkillTracker + skill 提示块。
// candidates 为空 → 返回空 tracker（freelancer 仍可在 loop 内 search_skill /
// load_skill 按需装载）。
// candidates 数量超出预算或 reader 缺失会立刻报错，让 Runtime.Run 在 LLM
// 启动前快速失败。
func hydrateSkills(reader *skill.Reader, candidates []string) (*tracker.SkillTracker, string, error) {
	tr := tracker.NewSkillTracker(freelancerSkillBudget)
	if len(candidates) == 0 {
		return tr, "", nil
	}
	if len(candidates) > freelancerSkillBudget {
		return nil, "", fmt.Errorf("candidate_skills 上限 %d，传了 %d", freelancerSkillBudget, len(candidates))
	}
	if reader == nil {
		return nil, "", fmt.Errorf("skill reader 未装配，无法解析 candidate_skills")
	}
	fulls := make([]*skill.SkillFull, 0, len(candidates))
	for _, name := range candidates {
		full, err := reader.Load(name)
		if err != nil {
			return nil, "", fmt.Errorf("candidate skill %q: %w", name, err)
		}
		fulls = append(fulls, full)
	}
	if err := tr.Preload(fulls); err != nil {
		return nil, "", err
	}
	return tr, prompt.BuildLoadedSkillsBlock(fulls), nil
}

// parseCandidateSkills 从 RunParams.Inputs["candidate_skills"] 抽数组。
// 非数组 / 非字符串元素 / nil 一律返回 nil（防御性 —— 上游 JSON unmarshal
// 没有严格校验）。
func parseCandidateSkills(inputs map[string]any) []string {
	if inputs == nil {
		return nil
	}
	raw, ok := inputs["candidate_skills"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// 编译期接口实现检查
var _ runtime.Runtime = (*LLM)(nil)
