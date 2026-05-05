// Package specialists implements the L1→L2 dispatch tool. emma calls
// `Specialists(task)` whenever a request needs professional execution
// (writing, research, code, analysis, multi-step coordination). The tool
// spawns the Specialists L2 sub-agent — itself an LLM agent — which then
// decomposes the task, dispatches L3 sub-agents via the Task tool,
// integrates the results, and returns a polished output for emma.
//
// Architecture position (3-tier):
//
//	L1 emma           — user-facing, persona + clarification
//	  └ Specialists   — coordinator/scheduler (this tool's spawn target)
//	       └ Agent → L3 sub-agents — actual execution (writer, researcher, ...)
//
// Implementation: thin wrapper over agent.AgentSpawner.SpawnSync — the
// real work happens inside the Specialists agent loop, configured via
// SpecialistsProfile + SpecialistsRole + specialistsPrinciples in the
// prompt/texts package.
package specialists

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the LLM-facing tool identifier emma sees.
const ToolName = "Specialists"

// SubagentType is the agent definition / profile name spawned by this tool.
// It must match the registered AgentDefinition.Name and the
// ResolveProfileBySubagentType case in prompt/profile.go.
const SubagentType = "specialists"

// Tool is emma's L2 dispatch tool.
type Tool struct {
	tool.BaseTool
	spawner agent.AgentSpawner
	logger  *zap.Logger
}

// New constructs a Specialists tool backed by the given AgentSpawner
// (typically the QueryEngine). logger may be nil.
func New(spawner agent.AgentSpawner, logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{
		spawner: spawner,
		logger:  logger.Named("specialists"),
	}
}

func (t *Tool) Name() string             { return ToolName }
func (t *Tool) Description() string      { return specialistsDescription }
func (t *Tool) IsReadOnly() bool         { return false }
func (t *Tool) IsConcurrencySafe() bool  { return false }
func (t *Tool) IsLongRunning() bool      { return true }
func (t *Tool) InputSchema() map[string]any {
	return inputSchema
}

// CheckPermission auto-allows. The Specialists agent and its spawned L3
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

// InterruptBehavior cancels the in-flight Specialists run on user interrupt.
func (t *Tool) InterruptBehavior() tool.InterruptMode {
	return tool.InterruptCancel
}

// MaxResultSizeChars caps the integrated output Specialists returns to emma.
func (t *Tool) MaxResultSizeChars() int { return 50000 }

// Execute spawns Specialists synchronously. emma's tool_call blocks until
// the L2 loop finishes and returns its <summary> + integrated output.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	startTime := time.Now()

	in, err := parseInput(raw)
	if err != nil {
		// Without this Warn log, a malformed Specialists call only shows
		// up in emma's tool_result — the operator has no way to see WHY
		// emma's dispatch was rejected without inspecting the WebSocket.
		t.logger.Warn("Specialists: parse input failed",
			zap.Error(err),
			zap.Int("raw_len", len(raw)),
			zap.String("raw_preview", truncate(string(raw), 200)),
		)
		return errResult("invalid input: " + err.Error()), nil
	}
	if err := in.validate(); err != nil {
		t.logger.Warn("Specialists: validate input failed",
			zap.Error(err),
			zap.Int("task_len", len(in.Task)),
		)
		return errResult(err.Error()), nil
	}

	// Forward parent-session context so events stitch back to emma's session
	// and lifecycle events reach the WebSocket client.
	parentSessionID := ""
	var parentOut chan<- types.EngineEvent
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		parentSessionID = tuc.Core.SessionID
	}
	if out, ok := tool.GetEventOut(ctx); ok {
		parentOut = out
	}

	cfg := &agent.SpawnConfig{
		Prompt:          in.Task,
		AgentType:       tool.AgentTypeSync,
		SubagentType:    SubagentType,
		Description:     defaultDescription(in.Description),
		Name:            "specialists",
		ParentSessionID: parentSessionID,
		ParentOut:       parentOut,
		Timeout:         15 * time.Minute, // L2 may run multiple L3 dispatches
	}

	// DEBUG: dispatch.in — what emma's LLM just handed to the Specialists
	// tool. Pair with `dispatch.out` below to see the round-trip; pair with
	// `spawn.start` / `spawn.end` (subagent.go) to see L2's interior. The
	// task_preview captures the full prompt body the L2 will receive.
	t.logger.Debug("dispatch.in",
		zap.String("tool", "Specialists"),
		zap.String("parent_session_id", parentSessionID),
		zap.Int("task_len", len(in.Task)),
		zap.String("task_preview", truncate(in.Task, 400)),
		zap.String("description", in.Description),
	)

	t.logger.Info("dispatch to specialists",
		zap.String("task", truncate(in.Task, 120)),
		zap.String("description", in.Description),
	)

	result, err := t.spawner.SpawnSync(ctx, cfg)
	if err != nil {
		t.logger.Error("specialists spawn failed",
			zap.Error(err),
			zap.Duration("duration", time.Since(startTime)),
		)
		return errResult(fmt.Sprintf("Specialists execution failed: %s", err.Error())), nil
	}

	t.logger.Info("specialists completed",
		zap.String("agent_id", result.AgentID),
		zap.String("status", result.Status),
		zap.Int("num_turns", result.NumTurns),
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("deliverables", len(result.Deliverables)),
	)

	metadata := map[string]any{
		"render_hint": "agent",
		"agent_id":    result.AgentID,
		"session_id":  result.SessionID,
		"status":      result.Status,
		"num_turns":   result.NumTurns,
		"duration_ms": time.Since(startTime).Milliseconds(),
	}
	if result.Usage != nil {
		metadata["input_tokens"] = result.Usage.InputTokens
		metadata["output_tokens"] = result.Usage.OutputTokens
	}
	if len(result.Deliverables) > 0 {
		metadata["deliverables"] = result.Deliverables
		metadata["has_deliverables"] = true
	}
	if result.Terminal != nil {
		metadata["terminal_reason"] = string(result.Terminal.Reason)
	}
	// Surface produced artifacts so the executor can lift them onto the
	// L1 tool.end event. Without this, the WebSocket sees a final
	// tool.end with empty artifacts and has to scrape sub-agent events
	// to know what was produced. Doc §10.
	if len(result.SubmittedArtifacts) > 0 {
		metadata["artifacts"] = result.SubmittedArtifacts
	}

	// Surface deliverable events so the WebSocket client can render files.
	if parentOut != nil && len(result.Deliverables) > 0 {
		for _, d := range result.Deliverables {
			d := d
			parentOut <- types.EngineEvent{
				Type:        types.EngineEventDeliverable,
				AgentID:     result.AgentID,
				AgentName:   "specialists",
				Deliverable: &d,
			}
		}
	}

	// On terminal failures (LLM error / prompt-too-long / blocking-limit /
	// contract violation cap), the sub-agent often produced no Output —
	// returning an empty Content with IsError=true gives emma's LLM no
	// information and tempts it to fabricate. Build a structured failure
	// report from result.Terminal.Message + ContractFailures instead.
	isError := agent.IsTerminalError(result)
	content := result.Output
	if isError {
		content = agent.BuildFailureContent(result, "Specialists")
		// Same "log content not just count" policy as Task tool.
		// terminal_message is the highest-signal field — it carries the
		// actual reason string from the engine ("L3 declined to submit
		// after 3 reminders" / "SubmitTaskResult rejected 3 times" / etc).
		var reason, msg string
		if result.Terminal != nil {
			reason = string(result.Terminal.Reason)
			msg = result.Terminal.Message
		}
		t.logger.Warn("Specialists: sub-agent failed",
			zap.String("agent_id", result.AgentID),
			zap.String("status", result.Status),
			zap.String("terminal_reason", reason),
			zap.String("terminal_message", truncate(msg, 200)),
			zap.Int("contract_failures", len(result.ContractFailures)),
			zap.Strings("failure_sample", failureSample(result.ContractFailures, 3)),
		)
	}

	// DEBUG: dispatch.out — exactly what emma's LLM will see as
	// tool_result.Content. This is the highest-signal log line for
	// diagnosing "emma fabricated content" / "emma can't find the
	// artifact" issues — if the artifact_id isn't in the preview, emma
	// can't reference it no matter how good her prompting is.
	t.logger.Debug("dispatch.out",
		zap.String("tool", "Specialists"),
		zap.Bool("is_error", isError),
		zap.Int("content_len", len(content)),
		zap.String("content_preview", truncate(content, 600)),
		zap.Int("submitted_artifacts", len(result.SubmittedArtifacts)),
		zap.Int("deliverables", len(result.Deliverables)),
		zap.Duration("duration", time.Since(startTime)),
	)

	return &types.ToolResult{
		Content:  content,
		IsError:  isError,
		Metadata: metadata,
	}, nil
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true}
}

func defaultDescription(desc string) string {
	if desc != "" {
		return desc
	}
	return "specialists run"
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

const specialistsDescription = `把"专业产出"任务派给 Specialists（L2 调度统筹者）。

任何"专业产出"——写作、报告、代码、数据分析、深度调研、多步协调——都用 Specialists。emma 不直接做这些，而是交给 Specialists：拆任务、派 L3 sub-agent（writer / researcher / analyst / developer / lifestyle / scheduler / general-purpose）、整合结果、做质量检查、返回打磨好的产出。

调用前 emma 的职责：
- 通过 AskUserQuestion 把歧义澄清干净（Specialists 不能向用户追问）。
- 可选地做 1-2 次轻量 WebSearch / TavilySearch 补背景。
- 把用户的原话 + 澄清后的答案原样转发——**不要**重组成"需求：1. 2. 3."，**不要**自己编出用户没要求的规范（字数、格式、章节、截止时间）。拆解和结构化是 Specialists 的事，不是 emma 的。

输入：
- task（必填）：澄清后的用户意图——原话或轻度规整后的散文，不是打包好的简报。保留用户语气，让 Specialists 自己做结构决策。
- description（可选）：3-5 词的标签，便于观测。

行为：
- 同步——会阻塞到 Specialists 跑完。
- 返回的产出以 <summary> 开头，附带产出文件 / artifact。
- 单步 / 多步任务都由 Specialists 自己判断处理，emma 不挑。

注意：
- Specialists 有自己的 LLM loop，内部用 Task 工具派 L3。
- Specialists 内的 sub-agent 不能递归调 Specialists 或 Orchestrate。
- Specialists 不能向用户追问（没有 AskUserQuestion 访问权）。`
