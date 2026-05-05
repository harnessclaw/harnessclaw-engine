package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

// maxSubAgentTurns is the hard upper limit for any sub-agent's MaxTurns,
// regardless of what SpawnConfig requests.
const maxSubAgentTurns = 25

// SpawnSync implements agent.AgentSpawner. It creates an ephemeral sub-agent
// session and runs a full query loop synchronously, blocking until completion.
//
// The 14-step flow follows the design doc §3.7:
//  1. Apply timeout
//  2. Cap MaxTurns
//  3. Generate sub-session ID
//  4. Create sub-session with metadata
//  5. Initialize conversation context (spawn vs fork)
//  6. Build filtered ToolPool
//  7. Resolve prompt profile
//  8. Build permission checker (InheritedChecker)
//  9. Create drain channel
//  10. Emit subagent.start on parent out (via eventBus)
//  11. Run query loop
//  12. Collect output
//  13. Emit subagent.end
//  14. Return SpawnResult
func (qe *QueryEngine) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (result *agent.SpawnResult, err error) {
	agentID := "agent_" + uuid.New().String()[:8]
	sessionID := cfg.ParentSessionID + "_sub_" + uuid.New().String()[:8]
	startTime := time.Now()

	logger := qe.logger.With(
		zap.String("agent_id", agentID),
		zap.String("sub_session_id", sessionID),
		zap.String("parent_session_id", cfg.ParentSessionID),
		zap.String("subagent_type", cfg.SubagentType),
		zap.Bool("fork", cfg.Fork),
	)

	// Panic recovery: convert panics to error result.
	defer func() {
		if r := recover(); r != nil {
			logger.Error("sub-agent panicked", zap.Any("panic", r))
			terminal := types.Terminal{
				Reason:  types.TerminalModelError,
				Message: fmt.Sprintf("internal error: sub-agent crashed: %v", r),
			}
			result = &agent.SpawnResult{
				Output:    fmt.Sprintf("internal error: sub-agent crashed: %v", r),
				Terminal:  &terminal,
				Usage:     &types.Usage{},
				SessionID: sessionID,
				AgentID:   agentID,
			}
			err = nil
		}
	}()

	logger.Info("spawning synchronous sub-agent")

	// DEBUG: spawn.start data-flow snapshot — what just came in over the
	// dispatch boundary. Counts only (no full prompt body) so the line
	// stays grep-friendly; full prompt is dumped via the LLM-request
	// debug line a few turns later. Pair with `spawn.end` below to see
	// both ends of one sub-agent execution.
	logger.Debug("spawn.start",
		zap.String("agent_id", agentID),
		zap.String("subagent_type", cfg.SubagentType),
		zap.String("name", cfg.Name),
		zap.String("description", cfg.Description),
		zap.Int("prompt_len", len(cfg.Prompt)),
		zap.String("prompt_preview", truncateForLog(cfg.Prompt, 200)),
		zap.Int("expected_outputs", len(cfg.ExpectedOutputs)),
		zap.Int("required_outputs", countRequired(cfg.ExpectedOutputs)),
		zap.String("task_id", cfg.TaskID),
		zap.Bool("fork", cfg.Fork),
		zap.Int("parent_messages", len(cfg.ParentMessages)),
		zap.Int("context_summary_len", len(cfg.ContextSummary)),
		zap.Int("allowed_skills", len(cfg.AllowedSkills)),
	)

	// Step 1: Apply timeout.
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	// Step 2: Cap MaxTurns.
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = qe.config.MaxTurns / 2
		if maxTurns < 5 {
			maxTurns = 5
		}
	}
	if maxTurns > maxSubAgentTurns {
		maxTurns = maxSubAgentTurns
	}

	// Step 3-4: Create sub-session with metadata.
	sess := &session.Session{
		ID:        sessionID,
		State:     session.StateActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]any{
			"parent_session_id": cfg.ParentSessionID,
			"agent_type":        string(cfg.AgentType),
			"is_sub_agent":      true,
			"fork":              cfg.Fork,
			"agent_id":          agentID,
		},
	}

	// Look up the AgentDefinition early so the preamble composer can render
	// the per-definition sub-agent contract (OutputSchema / Skills /
	// Limitations) for TierSubAgent. The lookup is reused below for tool
	// pool filtering and profile resolution, so this is just hoisting it.
	var agentDef *agent.AgentDefinition
	if qe.defRegistry != nil && cfg.SubagentType != "" {
		agentDef = qe.defRegistry.Get(cfg.SubagentType)
	}

	// InputSchema validation: when the definition declares an input contract
	// AND the caller provided structured inputs, validate before any LLM call.
	// A mismatch means the dispatcher constructed a bad request — fail fast
	// rather than letting the sub-agent guess from a partial prompt.
	if agentDef != nil && len(agentDef.InputSchema) > 0 && len(cfg.Inputs) > 0 {
		if fails := submittool.ValidateAgainstSchema(agentDef.InputSchema, cfg.Inputs); len(fails) > 0 {
			return nil, fmt.Errorf("input schema validation failed for %q: %s",
				cfg.SubagentType, strings.Join(fails, "; "))
		}
	}

	// Step 5a: Compose available-artifacts preamble. Doc §6 mode A —
	// instead of L2 pasting content into L3's prompt, the framework
	// surfaces a list of trace-scoped artifact metadata so L3 can
	// ArtifactRead what it actually needs. The preamble stays empty
	// when the trace has no artifacts yet, making this a no-op for
	// the common single-task path.
	artPreamble := qe.composeArtifactPreamble(ctx, logger)

	// Step 5a': Render the per-spawn deliverable contract (doc §3 M1).
	// Empty when the dispatcher didn't supply ExpectedOutputs, in which
	// case we don't add anything — keeps simple-task prompts identical
	// to the legacy path.
	contractPreamble := agent.RenderExpectedOutputs(cfg.ExpectedOutputs)

	// Step 5a'': Render the per-definition sub-agent contract (TierSubAgent
	// only). This carries the agent's PERMANENT contract — OutputSchema,
	// Skills, Limitations — distinct from the per-spawn ExpectedOutputs.
	// Without this preamble the L3 LLM has no way to learn its own output
	// shape from the registry, so it would either guess (often wrong) or
	// terminate on plain end_turn (rejected by the driver as not-yet-done).
	subAgentPreamble := agent.RenderSubAgentContract(agentDef)

	// composeUserMessage stacks the framework-injected blocks above the
	// per-mode body. Order matters — the LLM reads top-down:
	//   1. <available-artifacts>     — what the L3 may consume
	//   2. <sub-agent-contract>      — who I am + what shape I always produce
	//   3. <expected-outputs>        — what THIS task additionally requires
	//   4. <task> ... </task>        — the actual instruction
	// Identity / contract come before task-specific overlay, matching the
	// "general before specific" prompt-cache stability principle.
	composeUserMessage := func(body string) string {
		var parts []string
		if artPreamble != "" {
			parts = append(parts, artPreamble)
		}
		if subAgentPreamble != "" {
			parts = append(parts, subAgentPreamble)
		}
		if contractPreamble != "" {
			parts = append(parts, contractPreamble)
		}
		parts = append(parts, body)
		return joinNonEmpty(parts, "\n\n")
	}

	// Step 5b: Initialize conversation context.
	// Three modes:
	//   Fork:    full parent history + new prompt (maximum context, risk of attention dilution)
	//   Distill: compressed summary + new prompt (balanced: relevant context without noise)
	//   Spawn:   blank session + new prompt (minimum context, maximum focus)
	var systemPromptOverride string
	if cfg.Fork && len(cfg.ParentMessages) > 0 {
		// Fork mode: copy parent conversation and append new prompt.
		for _, pm := range cfg.ParentMessages {
			sess.AddMessage(types.Message{
				Role: types.Role(pm.Role),
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: pm.Content,
				}},
				CreatedAt: time.Now(),
			})
		}
		// In fork mode the new task message is a fresh user turn; wrap
		// it with <task> via WrapTaskWithPreamble so the LLM doesn't
		// confuse the artifact list with the inherited conversation.
		taskBody := artifact.WrapTaskWithPreamble(cfg.Prompt, nil, 0) // wrapper handles "no artifacts → passthrough"
		taskBody = composeUserMessage(taskBody)
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: taskBody,
			}},
			CreatedAt: time.Now(),
		})
		systemPromptOverride = cfg.SystemPromptOverride
	} else if cfg.ContextSummary != "" {
		// Distill mode: inject compressed context + task prompt as a single user
		// message. Using one message avoids wasting tokens on a synthetic assistant
		// turn and prevents the false-confirmation bias that "I understand" creates.
		// The XML tags let the model distinguish background from task instruction.
		distillPrompt := "<context-summary>\n" + cfg.ContextSummary + "\n</context-summary>\n\n<task>\n" + cfg.Prompt + "\n</task>"
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: composeUserMessage(distillPrompt),
			}},
			CreatedAt: time.Now(),
		})
	} else {
		// Spawn mode: blank session with just the prompt.
		// Wrap with <task> whenever the framework prepends ANY block —
		// either the artifact preamble or the expected-outputs contract.
		// Otherwise the LLM may treat the closing list as part of its
		// own instructions.
		body := cfg.Prompt
		if artPreamble != "" || contractPreamble != "" {
			body = "<task>\n" + cfg.Prompt + "\n</task>"
		}
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: composeUserMessage(body),
			}},
			CreatedAt: time.Now(),
		})
	}

	// Step 6: Build filtered ToolPool.
	//
	// Filtering policy:
	//   - If AgentDefinition.AllowedTools is non-empty, treat it as an
	//     authoritative whitelist that bypasses AgentType blacklist.
	//     This lets specialised agents like "specialists" (L2) re-enable
	//     tools that are blanket-blocked for sync sub-agents (e.g. Agent).
	//   - Otherwise apply the default AgentType-based blacklist.
	//
	// agentDef was looked up above (step 5 — we hoisted it so the preamble
	// composer could render the per-definition sub-agent contract).

	pool := tool.NewToolPool(qe.registry, nil, nil)

	// L3 sub-agents get their AllowedTools (when set) augmented with the
	// always-required terminal tools — SubmitTaskResult and EscalateToPlanner.
	// Without this, a worker definition that whitelists ["WebSearch"]
	// would have no way to submit OR escalate, and the driver would loop
	// to nudge cap and fail. The augment happens BEFORE FilterByNames so
	// the final pool definitively includes both.
	allowedTools := agentDef.MaybeAugmentForSubAgent()

	if len(allowedTools) > 0 {
		// Explicit whitelist — bypass AgentType blacklist entirely.
		pool = pool.FilterByNames(allowedTools)
		logger.Debug("tool pool restricted by agent definition whitelist",
			zap.String("agent", cfg.SubagentType),
			zap.Int("tools", pool.Size()),
			zap.Strings("allowed", allowedTools),
		)
	} else {
		// No whitelist — apply default AgentType blacklist.
		pool = pool.FilteredFor(cfg.AgentType)
	}

	// L3 invariant: dispatch tools are always stripped, even when not in
	// AllowedTools. Defense in depth — Validate now rejects them at
	// registration, but stale stored definitions might predate that check.
	isSubAgent := agentDef != nil && agentDef.EffectiveTier() == agent.TierSubAgent
	if isSubAgent {
		pool = pool.WithoutNames(dispatchToolNames)
		// P1-5: dangerous tools (Bash etc.) must be opt-in for sub-agents.
		// `keepList` is the agent's declared whitelist — any dangerous
		// tool NOT explicitly named there gets stripped. Effect: a worker
		// with empty AllowedTools (legacy default) has zero dangerous
		// tools regardless of what the AgentType blacklist let through.
		var keepList []string
		if agentDef != nil {
			keepList = agentDef.AllowedTools
		}
		pool = pool.WithoutDangerousUnless(keepList)
	}

	// Step 7: Resolve prompt profile.
	// Priority: AgentDefinition.Profile > subagentType string mapping > WorkerProfile.
	var profile *prompt.AgentProfile
	if agentDef != nil && agentDef.Profile != "" {
		profile = prompt.ResolveProfileByName(agentDef.Profile)
		logger.Debug("profile from agent definition", zap.String("profile", agentDef.Profile))
	}
	if profile == nil {
		profile = resolveSubAgentProfile(cfg.SubagentType)
	}

	// Step 8: Build permission checker.
	// Use InheritedChecker with parent's session-approved tools.
	var permChecker permission.Checker
	approvedTools := qe.getSessionApprovedTools(cfg.ParentSessionID)
	if len(approvedTools) > 0 {
		permChecker = permission.NewInheritedChecker(approvedTools)
	} else {
		permChecker = permission.BypassChecker{}
	}

	// Step 9: Build sub-agent engine config.
	subConfig := QueryEngineConfig{
		MaxTurns:             maxTurns,
		AutoCompactThreshold: qe.config.AutoCompactThreshold,
		ToolTimeout:          qe.config.ToolTimeout,
		MaxTokens:            qe.config.MaxTokens,
		SystemPrompt:         qe.config.SystemPrompt,
		ClientTools:          false, // sub-agents always server-side
	}

	// Build allowed skills map.
	// Priority: SpawnConfig.AllowedSkills > AgentDefinition.Skills > nil (all skills).
	var allowedSkills map[string]bool
	if len(cfg.AllowedSkills) > 0 {
		allowedSkills = make(map[string]bool, len(cfg.AllowedSkills))
		for _, s := range cfg.AllowedSkills {
			allowedSkills[s] = true
		}
	} else if agentDef != nil && len(agentDef.Skills) > 0 {
		allowedSkills = make(map[string]bool, len(agentDef.Skills))
		for _, s := range agentDef.Skills {
			allowedSkills[s] = true
		}
	}

	// Per-agent temperature / OutputSchema flow from registry to loop.
	// Both stay nil for definitions that don't set them, in which case
	// the loop and the SubmitTaskResult validator behave as before.
	var temperature *float64
	var outputSchema map[string]any
	if agentDef != nil {
		temperature = agentDef.Temperature
		outputSchema = agentDef.OutputSchema
	}

	lc := &loopConfig{
		pool:                 pool,
		profile:              profile,
		permChecker:          permChecker,
		config:               subConfig,
		systemPromptOverride: systemPromptOverride,
		subagentType:         cfg.SubagentType,
		allowedSkills:        allowedSkills,
		logger:               logger,
		agentID:              agentID,
		taskID:               cfg.TaskID,
		taskStartedAt:        cfg.TaskStartedAt,
		expectedOutputs:      cfg.ExpectedOutputs,
		temperature:          temperature,
		outputSchema:         outputSchema,
	}

	// Step 10: Emit subagent.start event.
	//
	// AgentTask carries the full prompt the parent dispatched. The client
	// can render it as "researcher 接到的任务：…" so the user sees what
	// each L3 was actually asked to do — without that, only the 3-5-word
	// AgentDesc reaches the wire and the sub-agent's actual mission is
	// invisible. We truncate at 800 runes so a long context-summary
	// preamble doesn't bloat the wire payload; the sub-agent's own loop
	// still receives the full prompt.
	if cfg.ParentOut != nil {
		cfg.ParentOut <- types.EngineEvent{
			Type:          types.EngineEventSubAgentStart,
			AgentID:       agentID,
			AgentName:     cfg.Name,
			AgentDesc:     cfg.Description,
			AgentTask:     truncateRunes(cfg.Prompt, 800),
			AgentType:     string(cfg.AgentType),
			ParentAgentID: cfg.ParentSessionID,
		}
	}
	if qe.eventBus != nil {
		qe.eventBus.Publish(event.Event{
			Topic: event.TopicSubAgentStarted,
			Payload: map[string]any{
				"agent_id":       agentID,
				"name":           cfg.Name,
				"description":    cfg.Description,
				"subagent_type":  cfg.SubagentType,
				"agent_type":     string(cfg.AgentType),
				"fork":           cfg.Fork,
				"parent_session": cfg.ParentSessionID,
			},
		})
	}

	// Step 11-12: Run query loop, drain events, collect text output.
	// Forward events to ParentOut for real-time client streaming.
	out := make(chan types.EngineEvent, 64)
	var loopResult subAgentLoopResult
	var textBuf strings.Builder
	var cumulativeUsage types.Usage
	var deliverables []types.Deliverable
	// producedArtifacts accumulates Refs from every tool_end event the
	// sub-agent emitted (the executor stamps them when render_hint=artifact).
	// Attached to the subagent_end event so the UI can render one card
	// listing "this sub-agent's outputs" without re-scanning the per-tool
	// stream. See doc §10.
	var producedArtifacts []types.ArtifactRef

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		// Tier-based driver routing. TierSubAgent runs the strict L3
		// driver (no further dispatch, mandatory submit-or-escalate);
		// every other tier — including the implicit default — runs the
		// existing coordinator loop unchanged.
		if isSubAgent {
			loopResult = qe.runSubAgentDriver(ctx, sess, lc, out)
		} else {
			loopResult = qe.runSubAgentLoop(ctx, sess, lc, out)
		}
	}()

	for evt := range out {
		switch evt.Type {
		case types.EngineEventText:
			textBuf.WriteString(evt.Text)
		case types.EngineEventToolEnd:
			// Detect deliverables: FileWrite tool_end with render_hint "file_info".
			if evt.ToolResult != nil && evt.ToolResult.Metadata != nil {
				if hint, _ := evt.ToolResult.Metadata["render_hint"].(string); hint == "file_info" {
					d := types.Deliverable{
						FilePath:  strVal(evt.ToolResult.Metadata, "file_path"),
						Language:  strVal(evt.ToolResult.Metadata, "language"),
						ByteSize:  intVal(evt.ToolResult.Metadata, "bytes_written"),
						ToolUseID: evt.ToolUseID,
					}
					if d.FilePath != "" {
						deliverables = append(deliverables, d)
					}
				}
			}
			// Aggregate artifacts the executor stamped on this tool_end
			// (single Ref per ArtifactWrite). Buffered until subagent_end
			// rather than emitted twice — the per-tool emission happens
			// further down when we forward as subagent_event.
			if len(evt.Artifacts) > 0 {
				producedArtifacts = append(producedArtifacts, evt.Artifacts...)
			}
		case types.EngineEventDone:
			if evt.Usage != nil {
				cumulativeUsage = *evt.Usage
			}
		}

		// Forward observability events to the parent's output channel.
		//
		// L1/L2 隔离：sub-agent LLM 文本 (EngineEventText) is intentionally
		// NOT forwarded — only the L1 main agent (emma) generates user-facing
		// prose. The spawning parent reads the sub-agent's output via
		// SpawnResult.Summary in the tool_result and polishes its own reply.
		//
		// Two forwarding paths:
		//
		//  1. THIS sub-agent's own tool lifecycle (ToolStart/ToolEnd) — wrap
		//     as SubAgentEvent stamped with this layer's agentID, so the
		//     parent can render "Specialists is calling Task / WebSearch".
		//
		//  2. Events that already came from a deeper layer (e.g. L3 events
		//     bubbling through L2 on their way to L1) — these arrive here as
		//     SubAgentStart/SubAgentEnd/SubAgentEvent/Deliverable and must
		//     be forwarded *as-is* with their original AgentID preserved.
		//     Without this, the WebSocket client never sees L3 lifecycle when
		//     L2 (Specialists) dispatches L3 via the Task tool.
		//
		// See docs/protocols/websocket.md v1.10.
		if cfg.ParentOut != nil {
			switch evt.Type {
			case types.EngineEventAgentIntent:
				// The sub-agent's executor stripped `intent` off the tool
				// input and emitted this — wrap it as subagent_event so it
				// reaches the wire stamped with this layer's agent identity
				// (mirroring how tool_start/tool_end are wrapped). The
				// client renders "researcher 正在搜 vLLM" without needing
				// to dig into the inner ToolInput JSON.
				cfg.ParentOut <- types.EngineEvent{
					Type:      types.EngineEventSubAgentEvent,
					AgentID:   agentID,
					AgentName: cfg.Name,
					SubAgentEvent: &types.SubAgentEventData{
						EventType: "intent",
						ToolName:  evt.ToolName,
						ToolUseID: evt.ToolUseID,
						Intent:    evt.Intent,
					},
				}
			case types.EngineEventToolStart:
				cfg.ParentOut <- types.EngineEvent{
					Type:      types.EngineEventSubAgentEvent,
					AgentID:   agentID,
					AgentName: cfg.Name,
					SubAgentEvent: &types.SubAgentEventData{
						EventType: "tool_start",
						ToolName:  evt.ToolName,
						ToolUseID: evt.ToolUseID,
						ToolInput: evt.ToolInput,
					},
				}
			case types.EngineEventToolEnd:
				inner := &types.SubAgentEventData{
					EventType: "tool_end",
					ToolName:  evt.ToolName,
					ToolUseID: evt.ToolUseID,
				}
				if evt.ToolResult != nil {
					inner.Output = evt.ToolResult.Content
					inner.IsError = evt.ToolResult.IsError
				}
				// Per-tool artifact surfacing (doc §10): if this tool_end
				// carries Refs, forward them at the inner-event level so
				// the UI can light up cards as each ArtifactWrite lands,
				// not only at the aggregated subagent_end.
				if len(evt.Artifacts) > 0 {
					inner.Artifacts = append([]types.ArtifactRef(nil), evt.Artifacts...)
				}
				cfg.ParentOut <- types.EngineEvent{
					Type:          types.EngineEventSubAgentEvent,
					AgentID:       agentID,
					AgentName:     cfg.Name,
					SubAgentEvent: inner,
				}
			case types.EngineEventSubAgentStart,
				types.EngineEventSubAgentEnd,
				types.EngineEventSubAgentEvent,
				types.EngineEventDeliverable:
				// Pass through unchanged — the deeper layer already stamped
				// the correct AgentID/AgentName, and re-wrapping would lose
				// that attribution. ParentAgentID stitches the chain back
				// together for the WebSocket client.
				cfg.ParentOut <- evt
			}
		}
	}
	<-done

	elapsed := time.Since(startTime)

	terminal := loopResult.Terminal

	// Step 13: Emit subagent.end event.
	agentStatus := "completed"
	switch terminal.Reason {
	case types.TerminalMaxTurns:
		agentStatus = "max_turns"
	case types.TerminalModelError:
		agentStatus = "model_error"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		agentStatus = "aborted"
	}
	// Prefer SubmitTaskResult-validated artifacts when present — that's
	// the canonical "deliverables" set. Fall back to the broader
	// produced-while-running list (everything any tool wrote) when the
	// task had no contract.
	endArtifacts := producedArtifacts
	if len(loopResult.SubmittedArtifacts) > 0 {
		endArtifacts = loopResult.SubmittedArtifacts
	}
	if cfg.ParentOut != nil {
		cfg.ParentOut <- types.EngineEvent{
			Type:        types.EngineEventSubAgentEnd,
			AgentID:     agentID,
			AgentName:   cfg.Name,
			AgentStatus: agentStatus,
			Duration:    elapsed.Milliseconds(),
			Usage:       &cumulativeUsage,
			Terminal: &types.Terminal{
				Reason: terminal.Reason,
				Turn:   terminal.Turn,
			},
			// Doc §10 aggregated form — every artifact this sub-agent
			// produced, in tool-call order. The UI uses this to render a
			// single "outputs" card on the sub-agent panel without having
			// to replay the per-tool stream.
			Artifacts: endArtifacts,
		}
	}
	if qe.eventBus != nil {
		qe.eventBus.Publish(event.Event{
			Topic: event.TopicSubAgentEnded,
			Payload: map[string]any{
				"agent_id":    agentID,
				"name":        cfg.Name,
				"reason":      string(terminal.Reason),
				"turns":       terminal.Turn,
				"duration_ms": elapsed.Milliseconds(),
			},
		})
	}

	// On non-success terminal, log at Warn with the actual reason /
	// message / first few failure strings — the Info-level summary alone
	// only carries counts, which forces every triage to dig through
	// dispatch.out / WebSocket to find what actually went wrong.
	completionFields := []zap.Field{
		zap.String("terminal_reason", string(terminal.Reason)),
		zap.String("terminal_message", truncateForLog(terminal.Message, 200)),
		zap.Int("turns", terminal.Turn),
		zap.Duration("elapsed", elapsed),
		zap.Int("submitted_artifacts", len(loopResult.SubmittedArtifacts)),
		zap.Int("contract_failures", len(loopResult.ContractFailures)),
	}
	if len(loopResult.ContractFailures) > 0 {
		completionFields = append(completionFields,
			zap.Strings("failure_sample", contractFailureSample(loopResult.ContractFailures, 3)))
	}
	if loopResult.NeedsPlanning {
		completionFields = append(completionFields,
			zap.Bool("needs_planning", true),
			zap.String("escalation_reason", truncateForLog(loopResult.EscalationReason, 200)))
	}
	if terminal.Reason == types.TerminalCompleted {
		logger.Info("sub-agent completed", completionFields...)
	} else {
		logger.Warn("sub-agent ended with non-success terminal", completionFields...)
	}

	// Step 14: Return SpawnResult with structured fields.
	// - Output: full text (stored in TaskRegistry for reference)
	// - Summary: extracted from <summary> tag (returned to the spawning parent in tool_result)
	// - Status: derived from terminal reason
	fullOutput := textBuf.String()
	summary := parseSummaryTag(fullOutput)

	// Derive status from terminal reason.
	status := "completed"
	switch terminal.Reason {
	case types.TerminalMaxTurns:
		status = "max_turns"
	case types.TerminalModelError:
		status = "error"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		status = "aborted"
	}

	// Build the output the spawning parent (typically the L1 main agent)
	// sees in tool_result. Three sections, top-down:
	//   1. <summary> — what the sub-agent reports it did
	//   2. 产出 artifact — IDs the parent can quote / ArtifactRead
	//   3. 产出文件 — FileWrite-side deliverables (legacy path)
	//
	// Why artifacts must surface here: the LLM only reads tool_result
	// content; subagent_end events go to the WebSocket client. Without
	// listing the IDs, emma has no way to reference produced artifacts
	// in her reply (e.g. "详情见 art_xxx") — she'd have to either
	// fabricate IDs or re-paste content. The `[role]` prefix lets emma
	// pick the right artifact when the contract had multiple roles.
	//
	// The full sub-agent transcript is preserved in TaskRegistry; this
	// view is intentionally narrow so emma's context stays tight.
	var parentVisibleOutput strings.Builder
	parentVisibleOutput.WriteString(summary)
	if refs := loopResult.SubmittedArtifacts; len(refs) > 0 {
		parentVisibleOutput.WriteString("\n\n产出 artifact：\n")
		for _, a := range refs {
			fmt.Fprintf(&parentVisibleOutput, "- [%s] %s — %s（%s, %s）\n",
				roleOrDash(a.Role),
				a.ArtifactID,
				artifactDisplayName(a),
				a.Type,
				artifact.HumanSize(a.SizeBytes),
			)
		}
	}
	if len(deliverables) > 0 {
		parentVisibleOutput.WriteString("\n产出文件：\n")
		for _, d := range deliverables {
			parentVisibleOutput.WriteString(fmt.Sprintf("- %s（%s，%d 字节）\n", d.FilePath, d.Language, d.ByteSize))
		}
	}

	// L3-only signal: when the driver flipped NeedsPlanning, override
	// status so the parent's tool_result content reflects it. The parent
	// LLM reads "needs_planning" and re-plans rather than treating an
	// escalation like a normal completion.
	if loopResult.NeedsPlanning {
		status = "needs_planning"
	}

	spawnResult := &agent.SpawnResult{
		Output:       parentVisibleOutput.String(),
		Summary:      summary,
		Status:       status,
		Attempts:     1, // TODO: increment when task-level retry is implemented
		Deliverables: deliverables,
		Terminal:     &terminal,
		Usage:        &cumulativeUsage,
		SessionID:    sessionID,
		AgentID:      agentID,
		NumTurns:     terminal.Turn,
		// Surface contract validation outcome (doc §3 M3/M4): the parent
		// reads these to decide between "integrate" / "retry" / "abort".
		SubmittedArtifacts: loopResult.SubmittedArtifacts,
		ContractFailures:   loopResult.ContractFailures,
		// L3 escalation surface (TierSubAgent driver only). Zero-valued
		// for L2 coordinator runs, populated when the L3 called
		// EscalateToPlanner instead of SubmitTaskResult.
		NeedsPlanning:      loopResult.NeedsPlanning,
		EscalationReason:   loopResult.EscalationReason,
		SuggestedNextSteps: loopResult.SuggestedNextSteps,
		SelfCheckFailures:  loopResult.SelfCheckFailures,
	}

	// Record full result in TaskRegistry for future reference (context passing, debugging).
	// Store the full output separately — the spawning parent only sees the summary.
	fullResult := *spawnResult
	fullResult.Output = fullOutput // preserve full sub-agent output
	qe.taskRegistryMu.Lock()
	qe.taskRegistry[agentID] = &fullResult
	qe.taskRegistryMu.Unlock()

	// DEBUG: spawn.end data-flow snapshot — pair with `spawn.start` to
	// see one full sub-agent run. parent_visible_preview shows EXACTLY
	// what the dispatching tool will hand back to the parent LLM as
	// tool_result.Content (after BuildFailureContent or summary+artifacts
	// composition). This is the load-bearing field for "did emma see
	// the artifacts?" debugging.
	logger.Debug("spawn.end",
		zap.String("agent_id", agentID),
		zap.String("status", status),
		zap.String("terminal_reason", string(terminal.Reason)),
		zap.Int("turns", terminal.Turn),
		zap.Duration("elapsed", elapsed),
		zap.Int("parent_visible_len", len(spawnResult.Output)),
		zap.String("parent_visible_preview", truncateForLog(spawnResult.Output, 400)),
		zap.Int("submitted_artifacts", len(spawnResult.SubmittedArtifacts)),
		zap.Strings("submitted_ids", refIDs(spawnResult.SubmittedArtifacts)),
		zap.Int("deliverables", len(spawnResult.Deliverables)),
		zap.Int("contract_failures", len(spawnResult.ContractFailures)),
	)

	return spawnResult, nil
}

// getSessionApprovedTools returns the list of tool names approved for a session.
func (qe *QueryEngine) getSessionApprovedTools(sessionID string) []string {
	qe.sessionAllowMu.RLock()
	defer qe.sessionAllowMu.RUnlock()
	tools, ok := qe.sessionAllowTools[sessionID]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(tools))
	for k := range tools {
		result = append(result, k)
	}
	return result
}

// loopConfig parameterizes the query loop for both main and sub-agent execution.
type loopConfig struct {
	pool                 *tool.ToolPool
	profile              *prompt.AgentProfile
	permChecker          permission.Checker
	config               QueryEngineConfig
	systemPromptOverride string
	subagentType         string          // agent definition name (e.g., "developer", "researcher")
	allowedSkills        map[string]bool // nil = all skills; non-nil = whitelist
	logger               *zap.Logger
	// agentID is the spawned sub-agent's identifier. Stamped onto every
	// artifact this loop writes so lineage (doc §4 producer.agent_id) is
	// preserved across spawn.
	agentID string
	// taskID is the orchestrator's identifier for this work unit. Empty
	// when no contract was supplied (legacy / simple-task path).
	taskID string
	// taskStartedAt anchors the temporal "no time travel" check applied
	// by SubmitTaskResult. Zero disables that check.
	taskStartedAt time.Time
	// expectedOutputs is the deliverable contract supplied by the parent.
	// Length 0 = no contract; loop terminates on end_turn as before.
	// Length > 0 = SubmitTaskResult required; loop refuses to terminate
	// without a passing submit (M3 + M4 from doc §3).
	expectedOutputs []types.ExpectedOutput
	// temperature overrides the LLM sampling temperature for this loop.
	// Nil leaves the request's Temperature at its zero value (provider
	// uses its own default). Sourced from AgentDefinition.Temperature
	// for sub-agents, nil for the legacy main-agent loop.
	temperature *float64
	// outputSchema is the per-agent declared structured output shape
	// (AgentDefinition.OutputSchema). When set the SubmitTaskResult
	// server-side validation matches submitted `result` against it.
	outputSchema map[string]any
}

// runSubAgentLoop is a variant of runQueryLoop parameterized by loopConfig.
// It uses the provided pool, profile, and permission checker instead of
// the engine's defaults.
//
// When lc.expectedOutputs is non-empty the loop refuses to terminate
// until SubmitTaskResult has been called AND its M4 validation passed.
// On end_turn without a passing submit, a SYSTEM reminder is appended
// and the loop continues; bounded by maxSubmitNudges to avoid spinning.
// Validation rejections from SubmitTaskResult itself are bounded by
// maxSubmitRejects independently.
func (qe *QueryEngine) runSubAgentLoop(
	ctx context.Context,
	sess *session.Session,
	lc *loopConfig,
	out chan<- types.EngineEvent,
) subAgentLoopResult {
	ls := &loopState{}
	logger := lc.logger

	// Sub-agent approval function auto-approves everything.
	approvalFn := func(_ context.Context, _ chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  true,
			Scope:     types.PermissionScopeOnce,
			Message:   "sub-agent auto-approved",
		}
	}
	executor := NewToolExecutor(lc.pool, lc.permChecker, logger, lc.config.ToolTimeout, approvalFn)
	if qe.artifactStore != nil {
		executor.SetArtifactStore(qe.artifactStore)
	}
	subProducer := tool.ArtifactProducer{
		AgentID:   lc.agentID,
		SessionID: sess.ID,
		// TaskID stamps every artifact this sub-agent writes with the
		// orchestrator-assigned task identifier. SubmitTaskResult's M4
		// validation rejects artifacts whose producer.task_id ≠ this
		// value, blocking failure mode #8 (claiming someone else's
		// artifact as your output).
		TaskID: lc.taskID,
	}
	if tc := emit.FromContext(ctx); tc != nil {
		subProducer.TraceID = tc.TraceID
	}
	executor.SetArtifactProducer(subProducer)
	executor.SetTaskContract(tool.TaskContract{
		TaskID:          lc.taskID,
		TaskStartedAt:   lc.taskStartedAt,
		ExpectedOutputs: lc.expectedOutputs,
		OutputSchema:    lc.outputSchema,
	})

	// Submission state. Tracked as the loop runs so we know whether
	// end_turn is acceptable and whether to nudge / fail.
	var (
		submitAccepted     bool
		submitArtifacts    []types.ArtifactRef
		submitNudges       int
		submitRejects      int
		contractFailures   []string
	)
	hasContract := len(lc.expectedOutputs) > 0

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		// Auto-compact if needed.
		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, lc.config.MaxTokens, lc.config.AutoCompactThreshold) {
			logger.Info("sub-agent auto-compact triggered", zap.Int("msg_count", len(messages)))
			compacted, err := qe.compactor.Compact(ctx, messages)
			if err != nil {
				logger.Warn("sub-agent auto-compact failed", zap.Error(err))
			} else {
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		// Check max turns.
		if ls.turn > lc.config.MaxTurns {
			return subAgentLoopResult{
				Terminal: types.Terminal{
					Reason:  types.TerminalMaxTurns,
					Message: fmt.Sprintf("sub-agent reached max turns (%d)", lc.config.MaxTurns),
					Turn:    ls.turn - 1,
				},
				ContractFailures: contractFailures,
			}
		}

		// Build system prompt.
		systemPrompt := lc.systemPromptOverride
		if systemPrompt == "" {
			systemPrompt = qe.buildSubAgentSystemPrompt(ctx, sess, messages, lc.profile, lc.subagentType, lc.allowedSkills, lc.pool)
		}

		req := &provider.ChatRequest{
			Messages:  messages,
			System:    systemPrompt,
			Tools:     lc.pool.Schemas(),
			MaxTokens: lc.config.MaxTokens,
		}
		if lc.temperature != nil {
			req.Temperature = *lc.temperature
		}

		logger.Debug("sub-agent LLM request",
			zap.Int("turn", ls.turn),
			zap.Int("message_count", len(messages)),
			zap.Int("tool_count", lc.pool.Size()),
		)

		// ---- Phase 2: LLM Call with retry ----
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     qe.provider.Name(),
		}

		llmResult := retryLLMCall(ctx, qe.provider, req, logger, out)

		if llmResult.streamErr != nil {
			llmErr := llmResult.streamErr
			out <- types.EngineEvent{Type: types.EngineEventError, Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "sub-agent cancelled", Turn: ls.turn}, ContractFailures: contractFailures}
			}
			logger.Error("sub-agent LLM call failed after retries", zap.Error(llmErr))
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalModelError, Message: llmErr.Error(), Turn: ls.turn}, ContractFailures: contractFailures}
		}

		// Events were already streamed in real-time by retryLLMCall.
		textBuf := llmResult.textBuf
		toolCalls := llmResult.toolCalls

		ls.stopReason = llmResult.stopReason
		if llmResult.lastUsage != nil {
			ls.lastUsage = llmResult.lastUsage
			ls.cumulativeUsage.InputTokens += llmResult.lastUsage.InputTokens
			ls.cumulativeUsage.OutputTokens += llmResult.lastUsage.OutputTokens
			ls.cumulativeUsage.CacheRead += llmResult.lastUsage.CacheRead
			ls.cumulativeUsage.CacheWrite += llmResult.lastUsage.CacheWrite
		}

		// Emit message lifecycle events.
		stopReason := ls.stopReason
		if stopReason == "" {
			if len(toolCalls) > 0 {
				stopReason = "tool_use"
			} else {
				stopReason = "end_turn"
			}
		}
		out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: stopReason, Usage: ls.lastUsage}
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}

		// Append assistant message to session.
		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): No tool calls = LLM tried to terminate ----
		if len(toolCalls) == 0 {
			// Legacy / no-contract path: end_turn is terminal.
			if !hasContract {
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalCompleted,
						Message: "sub-agent finished",
						Turn:    ls.turn,
					},
				}
			}
			// Contract path: only end_turn AFTER a passing submit terminates.
			if submitAccepted {
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalCompleted,
						Message: "sub-agent finished with passing submission",
						Turn:    ls.turn,
					},
					SubmittedArtifacts: submitArtifacts,
				}
			}
			// LLM tried to end without submitting → nudge (M2 from doc §3).
			submitNudges++
			if submitNudges > maxSubmitNudges {
				logger.Warn("sub-agent end_turn without submission, exceeded nudge cap",
					zap.Int("nudges", submitNudges),
				)
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalMaxTurns,
						Message: fmt.Sprintf("L3 declined to call SubmitTaskResult after %d reminders", maxSubmitNudges),
						Turn:    ls.turn,
					},
					ContractFailures: append(contractFailures,
						fmt.Sprintf("missing SubmitTaskResult after %d nudges", maxSubmitNudges)),
				}
			}
			logger.Info("nudging sub-agent to call SubmitTaskResult",
				zap.Int("nudge", submitNudges),
				zap.Int("cap", maxSubmitNudges),
			)
			sess.AddMessage(buildSubmitNudgeMessage(submitNudges, lc.expectedOutputs))
			continue
		}

		// ---- Phase 4: Server-side tool execution ----
		if ctx.Err() != nil {
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent cancelled before tool execution", Turn: ls.turn}, ContractFailures: contractFailures}
		}

		// Inject allowed skills into context so SkillTool can enforce the whitelist.
		execCtx := ctx
		if lc.allowedSkills != nil {
			execCtx = tool.WithAllowedSkills(execCtx, lc.allowedSkills)
		}

		// Sub-agents also honour per-tool client routing — AskUserQuestion
		// is filtered out of sub-agent pools by the AllAgentDisallowed
		// blacklist, but using the same dispatcher keeps the routing rule
		// in one place and makes future "must-route-to-client" tools
		// (e.g. user confirmations from a worker) work consistently.
		results := qe.dispatchToolBatch(execCtx, executor, lc.pool, toolCalls, out)

		if ctx.Err() != nil {
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent cancelled during tool execution", Turn: ls.turn}, ContractFailures: contractFailures}
		}

		// Append tool results to session.
		for i, tc := range toolCalls {
			toolMsg := types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type:       types.ContentTypeToolResult,
					ToolUseID:  tc.ID,
					ToolName:   tc.Name,
					ToolResult: results[i].Content,
					IsError:    results[i].IsError,
				}},
				CreatedAt: time.Now(),
			}
			sess.AddMessage(toolMsg)

			for _, nm := range results[i].NewMessages {
				sess.AddMessage(nm)
			}

			// Observe SubmitTaskResult outcomes — both accepted and
			// rejected cases land here. Render hint is the unique signal
			// the submit tool emits (decoupled from package import).
			if hint, _ := results[i].Metadata["render_hint"].(string); hint == "task_submission" {
				accepted, _ := results[i].Metadata["submission_accepted"].(bool)
				if accepted {
					submitAccepted = true
					if refs, ok := results[i].Metadata["submitted_artifacts"].([]types.ArtifactRef); ok {
						submitArtifacts = refs
					}
					logger.Info("submission accepted",
						zap.Int("artifacts", len(submitArtifacts)),
					)
				} else {
					submitRejects++
					if reason, ok := results[i].Metadata["reason"].(string); ok {
						contractFailures = append(contractFailures, reason)
					}
					logger.Info("submission rejected",
						zap.Int("reject_count", submitRejects),
						zap.Int("cap", maxSubmitRejects),
					)
					if submitRejects > maxSubmitRejects {
						return subAgentLoopResult{
							Terminal: types.Terminal{
								Reason:  types.TerminalMaxTurns,
								Message: fmt.Sprintf("SubmitTaskResult rejected %d times — abandoning task", submitRejects),
								Turn:    ls.turn,
							},
							ContractFailures: contractFailures,
						}
					}
				}
			}
		}

		// ---- Phase 5 (part B): Continue loop ----
	}
}

// buildSubmitNudgeMessage assembles the SYSTEM-style reminder injected
// when an L3 reaches end_turn without a passing SubmitTaskResult.
// Listed by [SYSTEM] tag so the LLM treats it as framework directive,
// not a user input. Includes the contract roles so the model has
// everything it needs to make progress.
func buildSubmitNudgeMessage(nudge int, outs []types.ExpectedOutput) types.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "[SYSTEM] 你尚未调用 SubmitTaskResult 提交产物。任务未完成，请立即调用 (提示 %d/%d)。\n",
		nudge, maxSubmitNudges)
	if nudge >= 2 {
		b.WriteString("强制提示：先用 ArtifactWrite 把每份产出写入 store，再用 SubmitTaskResult 提交 ID 列表。\n")
	}
	if nudge >= maxSubmitNudges {
		b.WriteString("这是最后一次机会，再不提交将判定任务失败。\n")
	}
	if len(outs) > 0 {
		b.WriteString("\n本任务必交 role 列表：\n")
		for _, o := range outs {
			if o.Required {
				fmt.Fprintf(&b, "- %s\n", o.Role)
			}
		}
	}
	return types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText,
			Text: b.String(),
		}},
		CreatedAt: time.Now(),
	}
}

// buildSubAgentSystemPrompt builds a system prompt for the sub-agent using
// the given profile, without touching the parent's prompt cache.
// subagentType is the agent definition name (e.g., "developer", "researcher")
// used to look up the worker's identity from the definition registry.
// When allowedSkills is non-nil, only the listed skills appear in the prompt.
// pool is the filtered ToolPool whose schemas the LLM actually sees — passed
// in so the rendered "# 可用工具" block matches the callable set rather than
// the global registry.
func (qe *QueryEngine) buildSubAgentSystemPrompt(
	_ context.Context,
	sess *session.Session,
	messages []types.Message,
	profile *prompt.AgentProfile,
	subagentType string,
	allowedSkills map[string]bool,
	pool *tool.ToolPool,
) string {
	if qe.promptBuilder == nil {
		return qe.config.SystemPrompt
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	// Build skill listing, filtering by allowedSkills if set.
	skillListing := qe.getSkillListingFiltered(allowedSkills)

	// Look up agent definition to build worker identity, tool filter, and skills.
	//
	// workerIdentity becomes PromptContext.SystemPromptOverride, which
	// (per builder.go) takes precedence over the profile's static
	// SectionOverrides["role"]. The decision tree:
	//
	//   1. def.SystemPrompt set                    → honour it (explicit author intent)
	//   2. def.IsTeamMember=true                   → BuildWorkerIdentity (personalised "你叫小林…")
	//   3. profile has no role SectionOverride     → BuildWorkerIdentity (otherwise the
	//                                                role section falls through to
	//                                                IdentitySection which returns the
	//                                                L1 emma persona — leaking emma's
	//                                                identity into L3 prompts)
	//   4. profile has role SectionOverride        → leave empty so the profile's
	//                                                static role text wins
	//                                                (Specialists / Explore / Plan)
	//
	// Without case 3, dispatching `general-purpose` (IsTeamMember=false,
	// WorkerProfile has no role override) would silently install emma's
	// IdentitySection content into the L3 role section.
	var workerIdentity string
	var def *agent.AgentDefinition
	if qe.defRegistry != nil && subagentType != "" {
		def = qe.defRegistry.Get(subagentType)
	}
	if def != nil {
		profileHasRoleOverride := profile != nil &&
			profile.SectionOverrides != nil &&
			profile.SectionOverrides["role"] != ""

		// Leaf isolation (your L3 design point #1):
		//   "L3 sub-agent 不知道 Emma 是谁"
		// For TierSubAgent we deliberately drop the leader name — the
		// identity reads "你叫小林，是团队的搭档" instead of "你叫小林，是
		// emma 团队的搭档". For coordinators (specialists / Plan / etc.)
		// we still surface the leader because they DO need to coordinate
		// with the user-facing agent.
		leaderName := qe.config.MainAgentDisplayName
		if def.EffectiveTier() == agent.TierSubAgent {
			leaderName = ""
		}
		switch {
		case def.SystemPrompt != "":
			workerIdentity = def.SystemPrompt
		case def.EffectiveTier() == agent.TierSubAgent:
			// L3 sub-agents are pure functional — no team affiliation, no
			// personality injection. BuildFunctionalIdentity generates a
			// task-focused identity that doesn't reference emma or the team.
			workerIdentity = texts.BuildFunctionalIdentity(
				def.DisplayName,
				def.Description,
			)
		case def.IsTeamMember:
			workerIdentity = texts.BuildWorkerIdentity(
				def.DisplayName,
				leaderName,
				def.Description,
				def.Personality,
			)
		case !profileHasRoleOverride:
			workerIdentity = texts.BuildWorkerIdentity(
				def.DisplayName,
				leaderName,
				def.Description,
				def.Personality,
			)
		}
	}

	// Inject the filtered tool set so ToolsSection renders only the tools
	// the LLM can actually call. Without this the prompt would list the
	// entire global registry while the schema list is restricted by
	// AgentDefinition.AllowedTools / AgentType blacklist — a mismatch that
	// wastes tokens and tempts the model into doomed tool calls.
	var availableTools []tool.Tool
	if pool != nil {
		availableTools = pool.All()
	}

	promptCtx := &prompt.PromptContext{
		SessionID:            sess.ID,
		Turn:                 len(messages),
		Session:              sess,
		Tools:                qe.registry,
		AvailableTools:       availableTools,
		TotalTokensUsed:      totalTokens,
		ContextWindowSize:    200000,
		Memory:               make(map[string]string),
		EnvInfo:              qe.getEnvSnapshot(),
		SkillListing:         skillListing,
		SystemPromptOverride: workerIdentity,
	}

	output, err := qe.promptBuilder.Build(promptCtx, profile)
	if err != nil {
		qe.logger.Warn("sub-agent prompt build failed, using fallback", zap.Error(err))
		return qe.config.SystemPrompt
	}

	result := output.ToSystemPrompt()
	qe.logger.Debug("========== SUB-AGENT SYSTEM PROMPT START ==========\n"+result+"\n========== SUB-AGENT SYSTEM PROMPT END ==========",
		zap.String("session_id", sess.ID),
		zap.String("profile", profile.Name),
		zap.Int("char_count", len(result)),
		zap.Int("estimated_tokens", prompt.EstimateTokens(result)),
		zap.Int("block_count", len(output.Blocks)),
	)

	return result
}

// resolveSubAgentProfile maps a subagent_type string to a prompt profile.
func resolveSubAgentProfile(subagentType string) *prompt.AgentProfile {
	return prompt.ResolveProfileBySubagentType(subagentType)
}

// subAgentLoopResult bundles the loop's terminal state with the
// contract-validation outcome (doc §3 mechanism M3/M4). SpawnSync reads
// it to populate SpawnResult.SubmittedArtifacts / ContractFailures so the
// parent agent gets a structured view of "what landed" without parsing
// streamed events.
//
// The L3 driver (runSubAgentDriver) reuses this type and additionally
// populates NeedsPlanning / EscalationReason / SuggestedNextSteps when an
// EscalateToPlanner call fired. The L2 path (runSubAgentLoop) leaves
// those fields zero-valued.
type subAgentLoopResult struct {
	Terminal           types.Terminal
	SubmittedArtifacts []types.ArtifactRef
	ContractFailures   []string

	// L3-only fields. Empty when the loop ran an L2 coordinator.
	NeedsPlanning      bool
	EscalationReason   string
	SuggestedNextSteps string
	SelfCheckFailures  []string
}

// maxSubmitNudges caps how many times the loop will re-prompt an L3 that
// reaches end_turn without a passing SubmitTaskResult. Doc §7 — three
// strikes covers honest mistakes (forgot, mis-roled, missing required)
// without giving an adversarial / broken model unlimited turns.
const maxSubmitNudges = 3

// maxSubmitRejects caps how many failed validations (M4) we accept
// before declaring the task lost. Distinct from nudges: a nudge happens
// when the LLM didn't call submit; a reject when it called submit but
// the artifacts didn't pass. Both share a cap of 3 — same logic, same
// philosophy.
const maxSubmitRejects = 3

// joinNonEmpty stitches non-empty strings with sep. Used to compose the
// task user message from preamble blocks where any block may be absent.
// strings.Join would emit duplicate separators when middle entries are "".
func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// roleOrDash returns the role string, falling back to "-" when empty.
// Used in the artifact list emma sees so the format stays uniform when
// some artifacts have a role (contract-mode submissions) and others
// don't (no-contract dispatches just produce refs).
func roleOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// artifactDisplayName picks the most human-readable label for the
// artifact line in the parent-visible output. Preference order:
//   1. Name — what the producer called the file ("q4-report.md")
//   2. Description — short human prose ("Q4 销量调研报告")
//   3. ID — last resort
// Without this, ListArtifacts entries with empty Name look like raw IDs.
func artifactDisplayName(a types.ArtifactRef) string {
	if a.Name != "" {
		return a.Name
	}
	if a.Description != "" {
		return a.Description
	}
	return a.ArtifactID
}

// countRequired returns how many ExpectedOutputs are marked Required.
// Used by the spawn.start debug log to preview the contract — operators
// can see at a glance "this dispatch demands 2 mandatory outputs".
func countRequired(outs []types.ExpectedOutput) int {
	n := 0
	for _, o := range outs {
		if o.Required {
			n++
		}
	}
	return n
}

// refIDs extracts just the artifact_ids from a Refs slice, suitable for
// dumping into a single zap.Strings field. Keeps the spawn.end log line
// compact while still letting operators trace artifacts across logs.
func refIDs(refs []types.ArtifactRef) []string {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ArtifactID)
	}
	return ids
}

// composeArtifactPreamble queries the artifact store for everything visible
// in the current trace and formats it as the doc §6.A preamble. Returns ""
// when the engine has no store wired, when ctx carries no trace, or when
// the trace has no artifacts yet — callers can safely concatenate.
//
// Failure to query is logged at warn level but never aborts the spawn:
// the preamble is a hint, not a load-bearing input. Falling through to an
// empty preamble keeps the L3 spawn path resilient to a transient store
// hiccup.
func (qe *QueryEngine) composeArtifactPreamble(ctx context.Context, logger *zap.Logger) string {
	if qe.artifactStore == nil {
		return ""
	}
	store, ok := qe.artifactStore.(artifact.Store)
	if !ok {
		// Engine was wired with a non-conforming value. Surface once at
		// warn so the misconfiguration is visible, then degrade gracefully.
		logger.Warn("artifact store has unexpected type; preamble disabled")
		return ""
	}
	tc := emit.FromContext(ctx)
	if tc == nil || tc.TraceID == "" {
		return ""
	}
	arts, err := store.List(ctx, &artifact.ListFilter{TraceID: tc.TraceID})
	if err != nil {
		logger.Warn("artifact list for preamble failed; skipping",
			zap.String("trace_id", tc.TraceID),
			zap.Error(err),
		)
		return ""
	}
	if len(arts) == 0 {
		return ""
	}
	// INFO log so the server log shows "L3 was handed N artifacts on spawn"
	// — without this the §6.A injection is invisible from outside. Listing
	// the IDs (capped) lets operators correlate the spawn line with later
	// ArtifactRead events from the L3.
	ids := make([]string, 0, len(arts))
	for i, a := range arts {
		if i >= 5 {
			break
		}
		ids = append(ids, a.ID)
	}
	logger.Info("artifact preamble injected for sub-agent",
		zap.String("trace_id", tc.TraceID),
		zap.Int("count", len(arts)),
		zap.Strings("ids", ids),
	)
	return artifact.RenderAvailableList(arts, artifact.DefaultPreambleMaxItems)
}

// strVal safely extracts a string from a metadata map.
func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// intVal safely extracts an int from a metadata map.
// Handles both int and float64 (JSON numbers decode as float64).
func intVal(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

// truncateRunes truncates s to at most n runes (NOT bytes), appending an
// ellipsis when truncation actually happened. Used for wire payloads
// where the source may be Chinese or other multibyte text — byte-level
// truncation would split a codepoint and produce \xe5 garbage.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 4 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// summaryTagRe matches <summary>...</summary> in sub-agent output.
// Uses (?s) so . matches newlines within the tag.
var summaryTagRe = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

// parseSummaryTag extracts the content of the first <summary> tag from text.
// Fallback: returns the first non-empty paragraph, truncated to 200 chars.
func parseSummaryTag(text string) string {
	if m := summaryTagRe.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}

	// Fallback: first non-empty paragraph.
	for _, para := range strings.Split(text, "\n\n") {
		p := strings.TrimSpace(para)
		if p != "" {
			if len([]rune(p)) > 200 {
				return string([]rune(p)[:200]) + "..."
			}
			return p
		}
	}
	return ""
}
