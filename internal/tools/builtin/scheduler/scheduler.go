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
	"strings"
	"time"

	"go.uber.org/zap"

	schedpkg "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// ToolName is the LLM-facing tool identifier emma sees.
const ToolName = "scheduler"

// SubagentType is the agent definition / profile name spawned by this tool.
// It must match the registered AgentDefinition.Name and the
// ResolveProfileBySubagentType case in prompt/profile.go.
const SubagentType = "scheduler"

// Tool is emma's L2 dispatch tool.
type Tool struct {
	tool.BaseTool
	sched  schedpkg.Scheduler
	defReg *agent.AgentDefinitionRegistry
	logger *zap.Logger
}

// New constructs a scheduler tool backed by the given scheduler.Scheduler.
// defReg is used to resolve the "scheduler" AgentDefinition; nil falls back
// to a synthetic minimal Definition.
func New(sched schedpkg.Scheduler, defReg *agent.AgentDefinitionRegistry, logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{
		sched:  sched,
		defReg: defReg,
		logger: logger.Named("scheduler"),
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

	// 父 session / event 通道
	var parentSess types.SessionID
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		parentSess = types.SessionID(tuc.Core.SessionID)
	}
	var events chan<- types.EngineEvent
	if out, ok := tool.GetEventOut(ctx); ok {
		events = out
	}

	// 查 "scheduler" agent definition；找不到用最小 fallback。
	var def agent.AgentDefinition
	if t.defReg != nil {
		if d := t.defReg.Get(SubagentType); d != nil {
			def = *d
		} else {
			def = agent.AgentDefinition{Name: SubagentType, AgentType: tool.AgentTypeSync}
		}
	} else {
		def = agent.AgentDefinition{Name: SubagentType, AgentType: tool.AgentTypeSync}
	}

	t.logger.Info("dispatch to scheduler",
		zap.String("task", truncate(in.Task, 120)),
		zap.String("description", in.Description),
	)

	res, err := t.sched.Dispatch(ctx, schedpkg.SpawnParams{
		Definition:  def,
		Prompt:      in.Task,
		Description: defaultDescription(in.Description),
		Parent:      &schedpkg.ParentRef{SessionID: parentSess},
		InvokedBy:   schedpkg.Invoker{Kind: schedpkg.InvokerLLM, Source: ToolName},
		Events:      events,
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
