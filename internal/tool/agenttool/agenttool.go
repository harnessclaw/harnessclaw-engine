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
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/tool"
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
	spawner agent.AgentSpawner
	logger  *zap.Logger
}

// New creates an AgentTool backed by the given AgentSpawner.
func New(spawner agent.AgentSpawner, logger *zap.Logger) *AgentTool {
	return &AgentTool{spawner: spawner, logger: logger}
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

	// Resolve agent type from subagent_type string.
	agentType := resolveAgentType(input.SubagentType)

	// Build spawn config.
	//
	// No wall-clock Timeout: a 5-minute parent deadline propagates down to
	// every LLM call the sub-agent makes; one slow Chat() (Xunfei "Engine
	// Busy" can return at 2m12s) can starve the rest of the run. Per-call
	// guards (LLMAPITimeout / LLMFirstByteTimeout), retry.Retryer, the
	// orphan watchdog with parent-chain heartbeats, and the step_decision
	// prompt already cover the failure modes the old timeout was guarding
	// against. User cancellation still flows through the parent ctx.
	cfg := &agent.SpawnConfig{
		Prompt:       input.Prompt,
		AgentType:    agentType,
		SubagentType: input.SubagentType,
		Description:  input.Description,
		Name:         input.Name,
		Model:        input.Model,
		Fork:         input.Fork,
		// Forward the deliverable contract (doc §3 mechanisms 3+4): when
		// declared, the framework refuses to terminate the L3 loop until
		// submit_task_result validates against this list. Empty = no
		// contract; sub-agent terminates on plain end_turn (legacy path).
		ExpectedOutputs: input.ExpectedOutputs,
		// TaskID gives every artifact this dispatch produces a uniform
		// producer.task_id stamp — submit_task_result's M4 validation
		// rejects refs whose stamp doesn't match. Generated here from
		// the tool_use_id so each Task tool invocation is its own task.
		TaskID:        deriveTaskID(ctx),
		TaskStartedAt: time.Now().UTC(),
	}

	// Extract parent session ID from context if available.
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		cfg.ParentSessionID = tuc.Core.SessionID
	}

	// Root session: prefer ctx's RootSessionID (propagated from SpawnSync when
	// this Task tool runs inside a sub-agent), fall back to ParentSessionID
	// (covers the L1→L2 case where the immediate parent is the root).
	rootSID, _ := sessionstats.RootSessionIDFromCtx(ctx)
	if rootSID == "" {
		rootSID = cfg.ParentSessionID
	}
	cfg.RootSessionID = rootSID

	// ParentAgentID is the dispatching agent's session id (== card id
	// in the wire envelope). Without this, the translator's
	// parentForSubAgent has no anchor to walk up to and falls back to
	// "most recent tool card" — which is emma's scheduler tool call,
	// so L3 freelancer renders as a sibling of L2 scheduler in the UI
	// instead of nested under it. sessionstats.WithSessionID is set by
	// the parent's WithSubAgentStats on its own ctx before dispatch, so
	// SessionIDFromCtx(ctx) here returns the immediate parent's sess.ID.
	if sid, ok := sessionstats.SessionIDFromCtx(ctx); ok && sid != "" {
		cfg.ParentAgentID = sid
	}

	// candidate_skills is freelancer-only. Stash into cfg.Inputs for SpawnSync
	// hydration. Non-freelancer dispatches that mistakenly include this field
	// get a Warn so the operator notices the misconfiguration.
	if len(input.CandidateSkills) > 0 {
		if input.SubagentType == "freelancer" {
			if cfg.Inputs == nil {
				cfg.Inputs = map[string]any{}
			}
			arr := make([]any, len(input.CandidateSkills))
			for i, s := range input.CandidateSkills {
				arr[i] = s
			}
			cfg.Inputs["candidate_skills"] = arr
		} else {
			t.logger.Warn("Task: candidate_skills ignored for non-freelancer subagent_type",
				zap.String("subagent_type", input.SubagentType),
				zap.Int("count", len(input.CandidateSkills)),
			)
		}
	}

	// Extract the parent event output channel so subagent events reach the client.
	if out, ok := tool.GetEventOut(ctx); ok {
		cfg.ParentOut = out
	}

	// DEBUG: dispatch.in — what L2 (or whoever called Task) handed to
	// this tool. Logs the full contract so operators can diagnose
	// "L3 didn't write the right artifact" by comparing the contract
	// the LLM passed against what came back in dispatch.out.
	t.logger.Debug("dispatch.in",
		zap.String("tool", "freelance"),
		zap.String("parent_session_id", cfg.ParentSessionID),
		zap.String("subagent_type", input.SubagentType),
		zap.String("name", input.Name),
		zap.Int("prompt_len", len(input.Prompt)),
		zap.String("prompt_preview", truncate(input.Prompt, 400)),
		zap.Int("expected_outputs", len(input.ExpectedOutputs)),
		zap.Strings("expected_roles", expectedRoleList(input.ExpectedOutputs)),
		zap.String("task_id", cfg.TaskID),
		zap.Bool("fork", input.Fork),
		zap.Bool("run_in_background", input.RunInBackground),
	)

	t.logger.Info("spawning sub-agent",
		zap.String("subagent_type", input.SubagentType),
		zap.String("description", input.Description),
		zap.String("name", input.Name),
		zap.String("model", input.Model),
		zap.Bool("fork", input.Fork),
		zap.Bool("run_in_background", input.RunInBackground),
	)

	// Async mode: launch in background, return agent ID immediately.
	if input.RunInBackground {
		asyncSpawner, ok := t.spawner.(agent.AsyncSpawner)
		if !ok {
			return &types.ToolResult{
				Content:   "async spawning not supported by this engine configuration",
				IsError:   true,
				ErrorType: types.ToolErrorInternal,
			}, nil
		}
		agentID, err := asyncSpawner.SpawnAsync(ctx, cfg)
		if err != nil {
			t.logger.Error("Task: async spawn failed",
				zap.Error(err),
				zap.String("subagent_type", input.SubagentType),
				zap.String("name", input.Name),
				zap.Duration("duration", time.Since(startTime)),
			)
			return &types.ToolResult{
				Content:   fmt.Sprintf("Failed to spawn async agent: %s", err.Error()),
				IsError:   true,
				ErrorType: types.ToolErrorInternal,
			}, nil
		}
		return &types.ToolResult{
			Content: fmt.Sprintf("Agent launched in background with ID: %s", agentID),
			Metadata: map[string]any{
				"render_hint": "agent",
				"agent_id":    agentID,
				"background":  true,
				"duration_ms": time.Since(startTime).Milliseconds(),
			},
		}, nil
	}

	// Sync mode: spawn and wait for the sub-agent to complete.
	result, err := t.spawner.SpawnSync(ctx, cfg)
	if err != nil {
		t.logger.Error("sub-agent spawn failed",
			zap.Error(err),
			zap.Duration("duration", time.Since(startTime)),
		)
		// Sub-agent failures from the spawner are typically transient
		// (model_error / network / contract). Mark dependency_fail so
		// the parent agent gets the right hint that an inner step
		// fell over, not a malformed input.
		return &types.ToolResult{
			Content:   fmt.Sprintf("Agent execution failed: %s", err.Error()),
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}, nil
	}

	t.logger.Info("sub-agent completed",
		zap.String("agent_id", result.AgentID),
		zap.String("session_id", result.SessionID),
		zap.Int("num_turns", result.NumTurns),
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("denied_tools", len(result.DeniedTools)),
	)

	// Build metadata for observability.
	metadata := map[string]any{
		"render_hint": "agent",
		"agent_id":    result.AgentID,
		"session_id":  result.SessionID,
		"num_turns":   result.NumTurns,
		"duration_ms": time.Since(startTime).Milliseconds(),
	}
	if result.Usage != nil {
		metadata["input_tokens"] = result.Usage.InputTokens
		metadata["output_tokens"] = result.Usage.OutputTokens
	}
	if len(result.DeniedTools) > 0 {
		metadata["denied_tools"] = result.DeniedTools
	}
	if result.Terminal != nil {
		metadata["terminal_reason"] = string(result.Terminal.Reason)
	}
	// Surface produced artifacts so the executor can lift them onto the
	// tool.end event. Same rationale as scheduler: gives the WebSocket
	// a single anchor point for "what came out of this Task call" without
	// the frontend having to aggregate from sub-agent events.
	if len(result.SubmittedArtifacts) > 0 {
		metadata["artifacts"] = result.SubmittedArtifacts
	}
	if len(result.Deliverables) > 0 {
		metadata["deliverables"] = result.Deliverables
		metadata["has_deliverables"] = true
	}

	// Emit deliverable events for each file produced by the sub-agent.
	if out, ok := tool.GetEventOut(ctx); ok && len(result.Deliverables) > 0 {
		for _, d := range result.Deliverables {
			d := d // capture
			out <- types.EngineEvent{
				Type:        types.EngineEventDeliverable,
				AgentID:     result.AgentID,
				AgentName:   cfg.Name,
				Deliverable: &d,
			}
		}
	}

	// Determine if the sub-agent ended in an error state. On hard errors
	// the loop often produced empty Output; returning that to the parent
	// LLM as IsError=true with no content tempts the parent to fabricate
	// "what happened". BuildFailureContent renders a structured report
	// (reason / detail / contract_failures) the parent can quote back
	// to the user honestly. See agent/failure.go.
	isError := agent.IsTerminalError(result)
	content := result.Output
	if isError {
		label := input.Name
		if label == "" {
			label = input.SubagentType
		}
		if label == "" {
			label = "sub-agent"
		}
		content = agent.BuildFailureContent(result, label)
		// Failure-side logging policy: log the actual reason / message /
		// first few failures, not just counts. Without this an operator
		// staring at logs sees "contract_failures=2" and has to dig into
		// the WebSocket / tool_result content to find what they were —
		// painful when iterating on scheduler prompts. The truncation
		// below stops a 50-failure cascade from blowing the log line.
		var reason, msg string
		if result.Terminal != nil {
			reason = string(result.Terminal.Reason)
			msg = result.Terminal.Message
		}
		t.logger.Warn("Task: sub-agent failed",
			zap.String("agent_id", result.AgentID),
			zap.String("subagent_type", input.SubagentType),
			zap.String("name", input.Name),
			zap.String("terminal_reason", reason),
			zap.String("terminal_message", truncate(msg, 200)),
			zap.Int("contract_failures", len(result.ContractFailures)),
			zap.Strings("failure_sample", failureSample(result.ContractFailures, 3)),
			zap.Bool("needs_planning", result.NeedsPlanning),
			zap.String("escalation_reason", truncate(result.EscalationReason, 200)),
		)
	}

	// DEBUG: dispatch.out — exactly what the calling LLM (typically L2
	// scheduler) will see as tool_result.Content. The
	// submitted_artifacts count is the field to watch: 0 with isError=false
	// means the L3 finished but nothing came back across the contract —
	// either the dispatch had no expected_outputs, or the framework's
	// gating let it through inappropriately.
	t.logger.Debug("dispatch.out",
		zap.String("tool", "freelance"),
		zap.String("subagent_type", input.SubagentType),
		zap.Bool("is_error", isError),
		zap.Int("content_len", len(content)),
		zap.String("content_preview", truncate(content, 600)),
		zap.Int("submitted_artifacts", len(result.SubmittedArtifacts)),
		zap.Int("deliverables", len(result.Deliverables)),
		zap.Int("contract_failures", len(result.ContractFailures)),
		zap.Duration("duration", time.Since(startTime)),
	)

	return &types.ToolResult{
		Content:  content,
		IsError:  isError,
		Metadata: metadata,
	}, nil
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
