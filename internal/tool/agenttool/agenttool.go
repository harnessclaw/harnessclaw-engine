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

// ToolName is the registered name of the Agent tool.
const ToolName = "Agent"

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
	}

	// Extract parent session ID from context if available.
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		cfg.ParentSessionID = tuc.Core.SessionID
	}

	// Extract the parent event output channel so subagent events reach the client.
	if out, ok := tool.GetEventOut(ctx); ok {
		cfg.ParentOut = out
	}

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

	// Determine if the sub-agent ended in an error state.
	isError := false
	if result.Terminal != nil {
		switch result.Terminal.Reason {
		case types.TerminalModelError, types.TerminalPromptTooLong, types.TerminalBlockingLimit:
			isError = true
		}
	}

	return &types.ToolResult{
		Content:  result.Output,
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

const agentToolDescription = `Launch a sub-agent to handle complex, multi-step tasks autonomously.

The Agent tool spawns specialized sub-agents that execute their own query loops
with filtered tool sets. Each sub-agent type has specific capabilities:

- general-purpose: Full tool access (minus recursive Agent calls). Use for
  tasks requiring file edits, bash commands, and multi-step reasoning.
- Explore: Read-only agent for codebase exploration. Has access to Glob, Grep,
  Read, and search tools. Use for finding files, understanding code structure.
- Plan: Planning agent with limited tools. Use for designing implementation
  approaches before coding.

Usage notes:
- Always include a short description summarizing what the agent will do.
- The sub-agent runs synchronously — this tool blocks until the agent completes.
- Sub-agents cannot spawn further sub-agents (no recursion).
- Sub-agents cannot prompt the user for input or approval.
- Provide clear, detailed prompts so the agent can work autonomously.`
