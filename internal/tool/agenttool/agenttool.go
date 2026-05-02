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
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the registered name of the dispatch tool.
//
// Renamed from "Agent" to "Task" to disambiguate from the L1/L2/L3 agent
// concept in the architecture: this tool's job is to create a *task* and
// hand it to a sub-agent. The package name (agenttool) is kept for
// historical continuity — only the LLM-facing string changed.
const ToolName = "Task"

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
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if err := input.validate(); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Resolve agent type from subagent_type string.
	agentType := resolveAgentType(input.SubagentType)

	// Build spawn config.
	cfg := &agent.SpawnConfig{
		Prompt:       input.Prompt,
		AgentType:    agentType,
		SubagentType: input.SubagentType,
		Description:  input.Description,
		Name:         input.Name,
		Model:        input.Model,
		Fork:         input.Fork,
		Timeout:      5 * time.Minute, // default 5min timeout per design doc
		// Forward the deliverable contract (doc §3 mechanisms 3+4): when
		// declared, the framework refuses to terminate the L3 loop until
		// SubmitTaskResult validates against this list. Empty = no
		// contract; sub-agent terminates on plain end_turn (legacy path).
		ExpectedOutputs: input.ExpectedOutputs,
		// TaskID gives every artifact this dispatch produces a uniform
		// producer.task_id stamp — SubmitTaskResult's M4 validation
		// rejects refs whose stamp doesn't match. Generated here from
		// the tool_use_id so each Task tool invocation is its own task.
		TaskID:        deriveTaskID(ctx),
		TaskStartedAt: time.Now().UTC(),
	}

	// Extract parent session ID from context if available.
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		cfg.ParentSessionID = tuc.Core.SessionID
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
		zap.String("tool", "Task"),
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
				Content: "async spawning not supported by this engine configuration",
				IsError: true,
			}, nil
		}
		agentID, err := asyncSpawner.SpawnAsync(ctx, cfg)
		if err != nil {
			return &types.ToolResult{
				Content: fmt.Sprintf("Failed to spawn async agent: %s", err.Error()),
				IsError: true,
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
		return &types.ToolResult{
			Content: fmt.Sprintf("Agent execution failed: %s", err.Error()),
			IsError: true,
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
	// tool.end event. Same rationale as Specialists: gives the WebSocket
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
		t.logger.Warn("sub-agent failed; surfacing structured error to parent",
			zap.String("agent_id", result.AgentID),
			zap.String("subagent_type", input.SubagentType),
			zap.Int("contract_failures", len(result.ContractFailures)),
		)
	}

	// DEBUG: dispatch.out — exactly what the calling LLM (typically L2
	// Specialists) will see as tool_result.Content. The
	// submitted_artifacts count is the field to watch: 0 with isError=false
	// means the L3 finished but nothing came back across the contract —
	// either the dispatch had no expected_outputs, or the framework's
	// gating let it through inappropriately.
	t.logger.Debug("dispatch.out",
		zap.String("tool", "Task"),
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
func resolveAgentType(subagentType string) tool.AgentType {
	switch subagentType {
	case "Explore", "explore":
		return tool.AgentTypeSync
	case "Plan", "plan":
		return tool.AgentTypeSync
	case "general-purpose", "":
		return tool.AgentTypeSync
	default:
		return tool.AgentTypeSync
	}
}

const agentToolDescription = `Create a task and dispatch it to a sub-agent that will execute it autonomously.

The Task tool spawns a specialized sub-agent (worker / explore / plan / specific
team member) that runs its own query loop with a filtered tool set. Each
sub-agent type has specific capabilities:

- general-purpose: Full tool access (minus recursive Task calls). Use for
  tasks requiring file edits, bash commands, and multi-step reasoning.
- Explore / researcher: Read-only sub-agent for exploration / research.
- Plan: Read-only sub-agent for designing implementation approaches.
- writer / analyst / developer / lifestyle / scheduler: Specialised
  team members for their respective domains.

Usage notes:
- Always include a short description summarizing the task.
- This tool runs synchronously — it blocks until the sub-agent completes.
- Sub-agents cannot recursively call Task on themselves (no infinite recursion).
- Sub-agents cannot prompt the user for input or approval.
- Provide clear, detailed prompts so the sub-agent can work autonomously.`
