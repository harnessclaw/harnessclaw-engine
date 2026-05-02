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
		return errResult("invalid input: " + err.Error()), nil
	}
	if err := in.validate(); err != nil {
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
		t.logger.Warn("specialists failed; surfacing structured error to parent",
			zap.String("agent_id", result.AgentID),
			zap.String("status", result.Status),
			zap.Int("contract_failures", len(result.ContractFailures)),
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

const specialistsDescription = `Delegate a professional task to Specialists — the L2 coordinator.

Use Specialists for any "professional output" — writing, reports, code, data analysis, deep research, multi-step coordination. emma never executes these directly; she hands them to Specialists, who decomposes the task, dispatches L3 sub-agents (writer / researcher / analyst / developer / lifestyle / scheduler / general-purpose), integrates results, performs quality checks, and returns a polished output.

emma's responsibility BEFORE calling this tool:
- Clarify ambiguity via AskUserQuestion (Specialists cannot ask the user)
- Optionally do 1-2 light WebSearch / TavilySearch lookups for context
- Forward the user's intent in their own words plus whatever clarification answers they gave — DO NOT restructure into "requirements: 1. 2. 3.", DO NOT invent specifications the user never asked for (length, format, section headings, deadlines). Decomposition and structuring are Specialists' job, not emma's.

Input:
- task (required): the user's clarified intent — verbatim or lightly normalized prose, not a packaged brief. Keeping the user's wording lets Specialists make its own structural choices.
- description (optional): a 3-5 word label for observability

Behaviour:
- Synchronous — blocks until Specialists finishes
- Returns the integrated output starting with a <summary> tag, plus any deliverables
- Handles single-step and multi-step tasks transparently — emma does not pick

Notes:
- Specialists has its own LLM loop and uses the Task tool internally to spawn L3.
- Sub-agents inside Specialists cannot recursively call Specialists or Orchestrate.
- Specialists cannot prompt the user (no AskUserQuestion access).`
