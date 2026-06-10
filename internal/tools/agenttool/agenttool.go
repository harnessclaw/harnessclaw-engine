// Package agenttool implements the Agent tool that spawns sub-agents.
//
// The Agent tool allows the LLM to delegate complex, multi-step tasks to
// specialized sub-agents. Each sub-agent runs its own query loop with a
// filtered tool pool and optional prompt profile customization.
//
// This mirrors src/tools/AgentTool/AgentTool.ts.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// ToolName is the registered name of the dispatch tool.
//
// Named "freelance" to mirror the L1→L2 "scheduler" naming: just as L1
// calls "scheduler" to hand off to L2, L2 calls "freelance" to hand off
// to L3. The package name (agenttool) is kept for historical continuity.
const ToolName = "freelance"

// AgentTool spawns sub-agents to handle complex, multi-step tasks.
type AgentTool struct {
	tool.BaseTool
	sched  scheduler.Scheduler
	defReg *definition.Registry
	logger *zap.Logger
}

// New creates an AgentTool backed by the given scheduler.Scheduler and
// AgentDefinition registry. defReg is used to resolve the AgentDefinition
// from the input.SubagentType string; nil falls back to a synthetic minimal
// Definition with just Name set.
func New(sched scheduler.Scheduler, defReg *definition.Registry, logger *zap.Logger) *AgentTool {
	return &AgentTool{sched: sched, defReg: defReg, logger: logger}
}

func (t *AgentTool) Name() string            { return ToolName }
func (t *AgentTool) Description() string     { return agentToolDescription }
func (t *AgentTool) IsReadOnly() bool         { return false }
func (t *AgentTool) IsConcurrencySafe() bool  { return true }
func (t *AgentTool) IsLongRunning() bool      { return true }

func (t *AgentTool) InputSchema() map[string]any {
	return inputSchema
}

// CheckPermission implements tool.PermissionPreChecker.
// Agent spawning is auto-allowed — no user confirmation needed.
func (t *AgentTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *AgentTool) ValidateInput(raw json.RawMessage) error {
	input, err := parseInput(raw)
	if err != nil {
		return err
	}
	return input.validate()
}

func (t *AgentTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	startTime := time.Now()

	input, err := parseInput(raw)
	if err != nil {
		// Without this log, a malformed Task input only shows up in the
		// LLM's tool_result (IsError=true content) — the operator running
		// the server has no way to see WHY the dispatch was malformed
		// without enabling provider-level request dumps. Surface it once
		// at Warn so logs alone tell the story.
		t.logger.Warn("Task: parse input failed",
			zap.Error(err),
			zap.Int("raw_len", len(raw)),
			zap.String("raw_preview", truncate(string(raw), 200)),
		)
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if err := input.validate(); err != nil {
		t.logger.Warn("Task: validate input failed",
			zap.Error(err),
			zap.String("subagent_type", input.SubagentType),
			zap.Int("prompt_len", len(input.Prompt)),
		)
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}

	// Resolve agent definition from SubagentType:
	//   1) registry lookup (preferred, uses real definition data: AllowedTools/Profile/etc)
	//   2) fallback to a synthetic minimal Definition with just Name + AgentType set
	def := t.resolveDefinition(input)

	// 透传父事件 channel —— sub-agent 事件实时到 client。
	var events chan<- types.EngineEvent
	if out, ok := tool.GetEventOut(ctx); ok {
		events = out
	}

	// 父身份完整三元组：SessionID + AgentID + StepID(tool_use_id)
	// 缺任意一项都会让 wire 翻译层把 sub-agent 卡挂到错误的父节点之下。
	parentRef := &scheduler.ParentRef{}
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

	// Inputs：候选 skill 仅 freelancer 使用
	var inputs map[string]any
	if len(input.CandidateSkills) > 0 {
		if input.SubagentType == "freelancer" {
			inputs = map[string]any{}
			arr := make([]any, len(input.CandidateSkills))
			for i, s := range input.CandidateSkills {
				arr[i] = s
			}
			inputs["candidate_skills"] = arr
		} else {
			t.logger.Warn("Task: candidate_skills ignored for non-freelancer subagent_type",
				zap.String("subagent_type", input.SubagentType),
				zap.Int("count", len(input.CandidateSkills)),
			)
		}
	}

	params := scheduler.SpawnParams{
		Definition:  def,
		Prompt:      input.Prompt,
		Name:        input.Name,
		Description: input.Description,
		Hints:       scheduler.Hints{Background: input.RunInBackground},
		Parent:      parentRef,
		InvokedBy:   scheduler.Invoker{Kind: scheduler.InvokerLLM, Source: ToolName},
		Inputs:      inputs,
		Events:      events,
		// MaxTurns=0 → 让 Runtime.LLM 走 Definition.MaxTurns → 兜底 30
		Overrides: scheduler.Overrides{Model: input.Model, MaxTurns: 0},
	}

	t.logger.Info("spawning sub-agent",
		zap.String("subagent_type", input.SubagentType),
		zap.String("description", input.Description),
		zap.String("name", input.Name),
		zap.String("model", input.Model),
		zap.Bool("run_in_background", input.RunInBackground),
	)

	res, err := t.sched.Dispatch(ctx, params)
	if err != nil {
		t.logger.Error("sub-agent spawn failed",
			zap.Error(err),
			zap.Duration("duration", time.Since(startTime)),
		)
		return &types.ToolResult{
			Content:   fmt.Sprintf("Agent execution failed: %s", err.Error()),
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}, nil
	}

	// 按 Outcome 类型分支：sync 返完整结果；async 返 launched 消息
	switch outcome := res.Outcome.(type) {
	case scheduler.AsyncOutcome:
		return &types.ToolResult{
			Content: fmt.Sprintf("Agent launched in background with ID: %s", res.AgentID),
			Metadata: map[string]any{
				"render_hint": "agent",
				"agent_id":    res.AgentID,
				"task_id":     res.TaskID,
				"background":  true,
				"output_file": outcome.OutputFile,
				"duration_ms": time.Since(startTime).Milliseconds(),
			},
		}, nil

	case scheduler.SyncOutcome:
		metadata := map[string]any{
			"render_hint": "agent",
			"agent_id":    res.AgentID,
			"task_id":     res.TaskID,
			"tool_calls":  outcome.ToolCalls,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"input_tokens":  res.Usage.InputTokens,
			"output_tokens": res.Usage.OutputTokens,
		}
		if outcome.Terminal.Reason != "" {
			metadata["terminal_reason"] = string(outcome.Terminal.Reason)
		}
		if len(outcome.DeniedTools) > 0 {
			metadata["denied_tools"] = outcome.DeniedTools
		}
		if len(outcome.Artifacts) > 0 {
			metadata["artifacts"] = outcome.Artifacts
		}
		if len(outcome.Deliverables) > 0 {
			metadata["deliverables"] = outcome.Deliverables
			metadata["has_deliverables"] = true
		}

		// 失败路径：Terminal 不是 completed 当作 error
		isError := outcome.Terminal.Reason != "" && outcome.Terminal.Reason != types.TerminalCompleted
		content := concatText(outcome.Content)
		if isError {
			content = formatFailure(input, outcome)
			t.logger.Warn("Task: sub-agent failed",
				zap.String("agent_id", string(res.AgentID)),
				zap.String("subagent_type", input.SubagentType),
				zap.String("terminal_reason", string(outcome.Terminal.Reason)),
				zap.String("terminal_message", truncate(outcome.Terminal.Message, 200)),
			)
		}

		t.logger.Info("sub-agent completed",
			zap.String("agent_id", string(res.AgentID)),
			zap.String("task_id", string(res.TaskID)),
			zap.Int("tool_calls", outcome.ToolCalls),
			zap.Duration("duration", time.Since(startTime)),
			zap.Int("denied_tools", len(outcome.DeniedTools)),
		)

		return &types.ToolResult{
			Content:  content,
			IsError:  isError,
			Metadata: metadata,
		}, nil

	default:
		return &types.ToolResult{
			Content:   "Agent: unexpected outcome type",
			IsError:   true,
			ErrorType: types.ToolErrorInternal,
		}, nil
	}
}

// resolveDefinition 从 registry 查找 SubagentType 对应的 AgentDefinition；
// 找不到时返回最小骨架 Definition（让 Runtime.LLM 用 AgentType 默认工具池）。
func (t *AgentTool) resolveDefinition(in *agentInput) definition.AgentDefinition {
	if t.defReg != nil {
		if def := t.defReg.Get(in.SubagentType); def != nil {
			return *def
		}
	}
	return definition.AgentDefinition{
		Name:      in.SubagentType,
		AgentType: resolveAgentType(in.SubagentType),
	}
}

// concatText 把 SyncOutcome.Content 的 text block 拼接成一段输出。
func concatText(blocks []types.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == types.ContentTypeText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// formatFailure 把失败 SyncOutcome 渲染成给父 LLM 看的 content。
// 不再调用 agent.BuildFailureContent —— 它依赖 ContractFailures / NeedsPlanning
// 等 L2-only 字段，新 Outcome 没有这些。简化为 reason + message 摘要。
func formatFailure(in *agentInput, outcome scheduler.SyncOutcome) string {
	label := in.Name
	if label == "" {
		label = in.SubagentType
	}
	if label == "" {
		label = "sub-agent"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s failed] reason=%s", label, outcome.Terminal.Reason)
	if outcome.Terminal.Message != "" {
		fmt.Fprintf(&sb, "\n%s", outcome.Terminal.Message)
	}
	if txt := concatText(outcome.Content); txt != "" {
		fmt.Fprintf(&sb, "\n\nLast output:\n%s", txt)
	}
	return sb.String()
}

// InterruptBehavior implements tool.InterruptibleTool.
// Returns cancel — user can cancel a running sub-agent.
func (t *AgentTool) InterruptBehavior() tool.InterruptMode {
	return tool.InterruptCancel
}

// MaxResultSizeChars implements tool.ResultSizeLimiter.
// Sub-agent output is capped at 50000 characters.
func (t *AgentTool) MaxResultSizeChars() int {
	return 50000
}

// truncate clips a string to at most n bytes, rune-safe, with a marker
// when it actually cut. Used in debug log fields so dumping a 50KB
// prompt body doesn't blow the log line.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + "...[truncated]"
}

// expectedRoleList yields just the role names from a contract, suitable
// for a single zap.Strings field. Lets ops see "this dispatch demanded
// findings_report + comparison_table" without parsing JSON.
func expectedRoleList(outs []types.ExpectedOutput) []string {
	roles := make([]string, 0, len(outs))
	for _, o := range outs {
		roles = append(roles, o.Role)
	}
	return roles
}

// failureSample returns up to n failure strings, each capped at 120 chars
// for log readability. Used in the Warn log when sub-agent fails — the
// list itself can be long, but the first 3 reasons are usually enough to
// see whether the failure is contract-shape (M4) / self-check / nudge cap
// / something else.
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

// deriveTaskID returns a stable identifier for this Task dispatch. We use
// the parent's tool_use_id (one per invocation of the Task tool) so the
// task_id stamp on every produced artifact maps 1:1 to the LLM's tool_use
// in the trace — debugging "which dispatch produced this artifact" then
// boils down to grepping the task_id. Falls back to a synthesised ID when
// no ToolUseContext is present (test paths).
func deriveTaskID(ctx context.Context) string {
	if tuc, ok := tool.GetToolUseContext(ctx); ok && tuc.Core.ToolCallID != "" {
		return "task_" + tuc.Core.ToolCallID
	}
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}

// resolveAgentType maps a subagent_type string to the tool.AgentType enum.
// All sub-agent kinds are sync today; the function is kept as the explicit
// extension point if that ever stops being true.
func resolveAgentType(_ string) tool.AgentType {
	return tool.AgentTypeSync
}

const agentToolDescription = `创建一个任务并派给 sub-agent 自主执行。

freelance 工具会启动一个专业 sub-agent（freelancer / Plan 或具体团队成员），sub-agent 跑自己的 query loop，带受限工具集。各类型能力：

- freelancer：通用 worker，能力由装载的 skill 决定，能调用文件 / shell / web 等常规工具。适用于需要文件编辑、shell 命令、多步推理的任务。
- Plan：只读型 sub-agent，用于方案设计、需求分析。
- writer / analyst / developer / lifestyle / scheduler：各自领域的专业团队成员。

使用规范：
- 必须传入简短的 description（3-5 词概括任务）。
- 本工具同步执行——会阻塞直到 sub-agent 跑完。
- sub-agent 不能递归调 Task（防止无限递归）。
- sub-agent 不能向用户追问或请求授权。
- prompt 写得清晰、详细，让 sub-agent 能独立完成任务。`
