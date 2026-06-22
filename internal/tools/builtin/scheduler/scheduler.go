// Package scheduler implements the L1→L2 dispatch tool. emma calls
// `scheduler(task)` whenever a request needs professional execution
// (writing, research, code, analysis, multi-step coordination). The tool
// spawns the scheduler L2 sub-agent — itself an LLM agent — that
// decomposes the task, dispatches the L3 sub-agent (freelancer) via the
// task tool, integrates results, and returns a polished output for emma.
//
// Architecture position (3-tier):
//
//	L1 emma         — user-facing, persona + clarification
//	  └ scheduler   — L2 coordinator (this tool's spawn target)
//	       └ task → freelancer L3 — actual execution
//
// Implementation: thin wrapper over agent.AgentSpawner.SpawnSync — the
// real work happens inside the scheduler agent loop, configured via
// SchedulerProfile + SchedulerRole + schedulerPrinciples in the
// prompt/texts package.
//
// CoordinatorMode (react / plan) is an OPS-only knob: it flows through
// tool.WithCoordinatorMode on the parent context, NOT through this tool's
// input schema. emma does not pick the mode. D-mode auto-escalation from
// react → plan is decided inside ReActCoordinator on recoverable failure;
// emma sees a single tool_result either way (the mode tag is exposed via
// metadata.coordinator_mode for telemetry only).
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	schedpkg "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// ToolName is the LLM-facing tool identifier emma sees.
const ToolName = "dispatch"

// DefaultSubagentType 是 input.SubagentType 校验失败时使用的兜底名 ——
// 实际上 input.validate() 已经拦截了空值，这个常量留作历史 fallback
// 标识 + 测试用。生产路径永远走 input.SubagentType。
const DefaultSubagentType = "freelancer"

// PlanReminderSink 是 dispatch tool 通知 emma "刚跑完一个 plan，路径在这"
// 的窄接口。emma.Engine 实现了它。接口方式避免 scheduler tool 包反向 import
// emma 包导致的 cycle —— tool 包永远不直接依赖 agent 包。
//
// MVP 实现里只有 plan agent 成功时调用 Set，其他 agent 完全不触碰本接口。
// 后续扩展（清理 / failover / per-session 失活）通过新增方法实现，旧 caller
// 不受影响。
type PlanReminderSink interface {
	// Set 记录一个新完成的 plan：sessionID 是 emma 主 session，planPath 是
	// plan agent 写出来的 plan.md 绝对路径。taskID 来自 dispatch 调用的
	// SyncOutcome，便于审计。
	Set(sessionID, planPath, taskID string)
}

// Tool is emma's L2 dispatch tool.
type Tool struct {
	tool.BaseTool
	sched      schedpkg.Scheduler
	defReg     *definition.Registry
	planSink   PlanReminderSink // nil 时直接跳过 plan reminder 写入逻辑
	logger     *zap.Logger
}

// New constructs a scheduler tool backed by the given scheduler.Scheduler.
// defReg is used to resolve the "scheduler" AgentDefinition; nil falls back
// to a synthetic minimal Definition.
//
// planSink 可为 nil —— 提供时在 plan agent 完成时记录 plan.md 路径，供
// emma 后续 user message 前置注入 plan reminder 用。MVP 期 emma 之外的
// caller 都可以传 nil。
func New(sched schedpkg.Scheduler, defReg *definition.Registry, planSink PlanReminderSink, logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{
		sched:    sched,
		defReg:   defReg,
		planSink: planSink,
		logger:   logger.Named("scheduler"),
	}
}

func (t *Tool) Name() string            { return ToolName }
func (t *Tool) Description() string     { return schedulerDescription }
func (t *Tool) IsReadOnly() bool        { return false }
func (t *Tool) IsConcurrencySafe() bool { return false }
func (t *Tool) IsLongRunning() bool     { return true }
func (t *Tool) InputSchema() map[string]any {
	return inputSchema
}

// CheckPermission auto-allows. The scheduler agent and its spawned L3
// children each go through the regular permission pipeline for their own
// tool calls.
func (t *Tool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *Tool) ValidateInput(raw json.RawMessage) error {
	in, err := parseInput(raw)
	if err != nil {
		return err
	}
	return in.validate()
}

// InterruptBehavior cancels the in-flight scheduler run on user interrupt.
func (t *Tool) InterruptBehavior() tool.InterruptMode {
	return tool.InterruptCancel
}

// MaxResultSizeChars caps the integrated output scheduler returns to emma.
//
// scheduler.principles forces the L2 summary to be 1-3 sentences plus
// artifact_id references — the real content is in deliverables / artifacts,
// not in this content string. 8000 chars (~2k zh tokens) gives enough room
// for a degraded text-only fallback (when no artifacts were produced) but
// keeps the cap aligned with the summary contract so a misbehaving L2 that
// dumps the full report into Content gets truncated, not silently shipped.
func (t *Tool) MaxResultSizeChars() int { return 8000 }

// Execute spawns the scheduler synchronously. emma's tool_call blocks until
// the L2 loop finishes and returns its <summary> + integrated output.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	startTime := time.Now()

	in, err := parseInput(raw)
	if err != nil {
		// Without this Warn log, a malformed scheduler call only shows
		// up in emma's tool_result — the operator has no way to see WHY
		// emma's dispatch was rejected without inspecting the WebSocket.
		t.logger.Warn("scheduler: parse input failed",
			zap.Error(err),
			zap.Int("raw_len", len(raw)),
			zap.String("raw_preview", truncate(string(raw), 200)),
		)
		return errTypedResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if err := in.validate(); err != nil {
		t.logger.Warn("scheduler: validate input failed",
			zap.Error(err),
			zap.Int("task_len", len(in.Task)),
		)
		return errTypedResult(err.Error(), types.ToolErrorInvalidInput), nil
	}

	// Plan mode 在 scheduler 重构过渡期不可用 ——
	// L2 kernel 已删除；plan 编排能力在 future coordinator 模块重建之前不提供。
	if mode := tool.GetCoordinatorMode(ctx); mode == "plan" {
		return errTypedResult(
			"plan coordinator mode unavailable during scheduler refactor transition",
			types.ToolErrorInternal,
		), nil
	}

	// 父身份完整三元组：SessionID + AgentID + StepID(tool_use_id)
	// 缺 StepID 会让 L2 scheduler 卡错挂到 emma turn 根上而非 emma's scheduler tool_use 下。
	parentRef := &schedpkg.ParentRef{}
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		parentRef.SessionID = types.SessionID(tuc.Core.SessionID)
		parentRef.StepID = tuc.Core.ToolCallID
	}
	if sid, ok := sessionstats.SessionIDFromCtx(ctx); ok && sid != "" {
		parentRef.AgentID = types.AgentID(sid)
	}
	if rootSID, ok := sessionstats.RootSessionIDFromCtx(ctx); ok && rootSID != "" {
		parentRef.RootSessionID = types.SessionID(rootSID)
	}
	var events chan<- types.EngineEvent
	if out, ok := tool.GetEventOut(ctx); ok {
		events = out
	}

	// 按 emma 显式指定的 subagent_type 派遣。
	// 找不到时直接报错（不再回退到 freelancer）—— LLM 必须从 system prompt
	// 的搭档名册里挑名字，挑错就让它在下一轮 tool_result 看到错误自己纠正。
	if t.defReg == nil {
		return errTypedResult(
			"agent definition registry not wired; dispatch unavailable",
			types.ToolErrorInternal,
		), nil
	}
	d := t.defReg.Get(in.SubagentType)
	if d == nil {
		t.logger.Warn("dispatch: unknown subagent_type",
			zap.String("subagent_type", in.SubagentType),
		)
		return errTypedResult(
			fmt.Sprintf("unknown subagent_type %q; pick one from the team roster in your system prompt", in.SubagentType),
			types.ToolErrorInvalidInput,
		), nil
	}
	def := *d

	t.logger.Info("dispatch directly to L3",
		zap.String("subagent_type", in.SubagentType),
		zap.String("task", truncate(in.Task, 120)),
		zap.String("description", in.Description),
	)

	res, err := t.sched.Dispatch(ctx, schedpkg.SpawnParams{
		Definition:  def,
		Prompt:      in.Task,
		Description: defaultDescription(in.Description),
		Parent:      parentRef,
		InvokedBy:   schedpkg.Invoker{Kind: schedpkg.InvokerLLM, Source: ToolName},
		Events:      events,
		// L3 worker 用 Definition.MaxTurns（freelancer def 自己的预算），不显式 overrides。
		// Definition 未设时 Runtime.LLM 兜底 30。
	})
	if err != nil {
		t.logger.Error("scheduler spawn failed",
			zap.Error(err),
			zap.Duration("duration", time.Since(startTime)),
		)
		return errTypedResult(fmt.Sprintf("scheduler execution failed: %s", err.Error()), types.ToolErrorDependencyFail), nil
	}

	sync, ok := res.Outcome.(schedpkg.SyncOutcome)
	if !ok {
		return errTypedResult("scheduler: expected SyncOutcome", types.ToolErrorInternal), nil
	}

	t.logger.Info("scheduler completed",
		zap.String("agent_id", string(res.AgentID)),
		zap.String("task_id", string(res.TaskID)),
		zap.Int("tool_calls", sync.ToolCalls),
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("deliverables", len(sync.Deliverables)),
	)

	// Plan Mode reminder: 当 plan agent 成功完成且 Deliverables 里有 plan.md
	// 文件时，把路径写入 PlanReminderSink。emma 在下一轮 user message 处理时
	// 会把该 plan 全文前置注入到 user message 头部，让 LLM 多 turn 对话里
	// 始终看到正确的 plan path。
	//
	// 触发条件三选一全满足才写入：
	//   1. planSink 非 nil（main.go 装配时注入了）
	//   2. subagent_type == "plan"
	//   3. Terminal.Reason 是 TerminalCompleted（不写失败的 plan）
	if t.planSink != nil && in.SubagentType == "plan" && sync.Terminal.Reason == types.TerminalCompleted {
		planPath := findPlanFile(sync.Deliverables)
		if planPath != "" {
			rootSID, _ := sessionstats.RootSessionIDFromCtx(ctx)
			if rootSID != "" {
				t.planSink.Set(rootSID, planPath, string(res.TaskID))
				t.logger.Info("plan reminder registered",
					zap.String("root_session_id", rootSID),
					zap.String("plan_path", planPath),
					zap.String("task_id", string(res.TaskID)),
				)
			}
		}
	}

	metadata := map[string]any{
		"render_hint":   "agent",
		"agent_id":      res.AgentID,
		"task_id":       res.TaskID,
		"tool_calls":    sync.ToolCalls,
		"duration_ms":   time.Since(startTime).Milliseconds(),
		"input_tokens":  res.Usage.InputTokens,
		"output_tokens": res.Usage.OutputTokens,
	}
	if sync.Terminal.Reason != "" {
		metadata["terminal_reason"] = string(sync.Terminal.Reason)
	}
	if len(sync.Deliverables) > 0 {
		metadata["deliverables"] = sync.Deliverables
		metadata["has_deliverables"] = true
	}
	if len(sync.Artifacts) > 0 {
		metadata["artifacts"] = sync.Artifacts
	}
	// 注意：CoordinatorMode / EscalatedFromMode / BudgetSpent metadata
	// 已删除——这些来自 L2 kernel 的 plan/react 协调路径，重构后不再适用。

	isError := sync.Terminal.Reason != "" && sync.Terminal.Reason != types.TerminalCompleted
	content := concatText(sync.Content)
	if !isError {
		// 成功路径在尾注追加 dispatch metadata。sub-agent 自由文本里不一定
		// 提到 task_id（emma 凭空猜 → promote 用错 id 必失败），所以把
		// task_id（必备）+ outputs basename（best-effort）显式拼到末尾，让
		// emma 后续调 promote 时能直接抄。
		content = appendDispatchMeta(content, string(res.TaskID), sync.Deliverables)
	}
	if isError {
		content = fmt.Sprintf("[scheduler failed] reason=%s\n%s\n\nLast output:\n%s",
			sync.Terminal.Reason, sync.Terminal.Message, content)
		t.logger.Warn("scheduler: sub-agent failed",
			zap.String("agent_id", string(res.AgentID)),
			zap.String("terminal_reason", string(sync.Terminal.Reason)),
			zap.String("terminal_message", truncate(sync.Terminal.Message, 200)),
		)
	}

	return &types.ToolResult{
		Content:  content,
		IsError:  isError,
		Metadata: metadata,
	}, nil
}

// findPlanFile 从 Deliverables 列表里找 plan agent 写出来的 plan.md。
//
// 优先匹配文件名严格等于 "plan.md"（plan agent principles 强制约定的
// 落盘名）；找不到时回退匹配任何 .md 后缀的 deliverable（容忍 LLM
// 拼路径时的微小偏差）。MVP 期最严，只取第一个命中。
func findPlanFile(deliverables []types.Deliverable) string {
	var fallback string
	for _, d := range deliverables {
		if d.FilePath == "" {
			continue
		}
		base := filepath.Base(d.FilePath)
		if base == "plan.md" {
			return d.FilePath
		}
		if fallback == "" && strings.HasSuffix(strings.ToLower(base), ".md") {
			fallback = d.FilePath
		}
	}
	return fallback
}

// concatText 拼接 SyncOutcome.Content 的 text blocks。
func concatText(blocks []types.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == types.ContentTypeText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// appendDispatchMeta 在 sub-agent 自由文本末尾追加 dispatch 元信息块，
// 让父 LLM（emma）能稳定看到 task_id 和已落地的 outputs basename。
// emma 调 promote 必须用这个 task_id；自由文本里不一定提到，所以这里
// 强制塞一份。
//
// 输出格式（fence 块便于 emma 区分 sub-agent 文本 vs framework 元信息）：
//
//	<dispatch-meta>
//	task_id: t-ae15b37c-7d6
//	outputs:
//	  - AI赋能职场.txt
//	</dispatch-meta>
func appendDispatchMeta(content, taskID string, deliverables []types.Deliverable) string {
	if taskID == "" {
		return content
	}
	var sb strings.Builder
	sb.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n<dispatch-meta>\ntask_id: ")
	sb.WriteString(taskID)
	if names := deliverableBasenames(deliverables); len(names) > 0 {
		sb.WriteString("\noutputs:")
		for _, n := range names {
			sb.WriteString("\n  - ")
			sb.WriteString(n)
		}
	}
	sb.WriteString("\n</dispatch-meta>")
	return sb.String()
}

// deliverableBasenames 提取每个 Deliverable.FilePath 的文件名（去重保序，
// 跨平台路径分隔符容忍）。promote 工具的 source 字段要求 basename。
func deliverableBasenames(deliverables []types.Deliverable) []string {
	if len(deliverables) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(deliverables))
	out := make([]string, 0, len(deliverables))
	for _, d := range deliverables {
		if d.FilePath == "" {
			continue
		}
		base := d.FilePath
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		if base == "" {
			continue
		}
		if _, ok := seen[base]; ok {
			continue
		}
		seen[base] = struct{}{}
		out = append(out, base)
	}
	return out
}

// errResult is a short-form failure helper. Defaults ErrorType to
// Internal so callers that don't know better still produce a valid
// structured error; specific paths that DO know (input parse, validate)
// should use errTypedResult.
func errResult(msg string) *types.ToolResult {
	return errTypedResult(msg, types.ToolErrorInternal)
}

func errTypedResult(msg string, errType types.ToolErrorType) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: errType}
}

func defaultDescription(desc string) string {
	if desc != "" {
		return desc
	}
	return "scheduler run"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// failureSample returns up to n failure strings, each capped for log
// readability. Used by the failure-side Warn log so operators see the
// first few actual reasons (M4 / self-check / nudge cap, etc.) without
// the line length blowing up on a 50-failure cascade.
func failureSample(failures []string, n int) []string {
	if len(failures) <= n {
		out := make([]string, len(failures))
		for i, f := range failures {
			out[i] = truncate(f, 120)
		}
		return out
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = truncate(failures[i], 120)
	}
	return out
}

const schedulerDescription = `把"专业产出"任务派给 scheduler（L2 调度统筹者）。

任何"专业产出"——写作、报告、代码、数据分析、深度调研、多步协调——都用 scheduler。emma 不直接做这些，而是交给 scheduler：拆任务、派 L3 sub-agent、整合结果、做质量检查、返回打磨好的产出。

调用前 emma 的职责：
- 通过 ask_user_question 把歧义澄清干净（scheduler 不能向用户追问）。
- 可选地做 1-2 次轻量 web_search / tavily_search 补背景。
- 把用户的原话 + 澄清后的答案原样转发——**不要**重组成"需求：1. 2. 3."，**不要**自己编出用户没要求的规范（字数、格式、章节、截止时间）。拆解和结构化是 scheduler 的事，不是 emma 的。

输入：
- task（必填）：澄清后的用户意图——原话或轻度规整后的散文，不是打包好的简报。保留用户语气，让 scheduler 自己做结构决策。
- description（可选）：3-5 词的标签，便于观测。

行为：
- 同步——会阻塞到 scheduler 跑完。
- 返回 <summary>（1-3 句结论 + artifact_id 引用），具体产物在 artifact / deliverable 里。
- 单步 / 多步任务都由 scheduler 自己判断处理，emma 不挑也不挑 sub-agent。`
