package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

// dispatchToolNames lists the tools that bridge to other agents. L3
// sub-agents are leaves — by user spec point #2 they must NOT call any of
// these. The driver strips them from the pool defensively even when
// AgentDefinition.AllowedTools is misconfigured.
//
// Keep this list in lockstep with the dispatch surface: any new tool that
// spawns work belongs here too.
var dispatchToolNames = []string{"freelance", "scheduler"}

// runSubAgentDriver is the dedicated L3 ReAct executor. It enforces every
// invariant the user's L3 design called out:
//
//   - No further dispatch — dispatchToolNames pre-stripped from the pool.
//   - Mandatory submission — TierSubAgent always has hasContract=true; the
//     loop refuses to terminate without submit_task_result OR escalate_to_planner.
//   - Stateless protocol — escalation goes via the escalate_to_planner tool
//     and surfaces as subAgentLoopResult.NeedsPlanning, never through
//     internal state pinned to the engine.
//   - Bounded retries — same maxSubmitNudges / maxSubmitRejects ceilings
//     used by runSubAgentLoop, so a stuck L3 fails fast.
//
// What this driver intentionally does NOT do:
//
//   - Cross-agent calls (banned per spec)
//   - Mailbox handling (L1 concept; L3 is synchronous from L2's POV)
//   - Permission UI (L3 inherits parent's session-approved tools at most)
//
// The 5-phase shape (preprocess / LLM / error / tool / continuation) is
// the same as runSubAgentLoop — what changes is the termination policy
// and the post-tool result inspection. Helpers callLLM,
// dispatchToolBatch, buildAssistantMessage are reused unchanged.
func (qe *QueryEngine) runSubAgentDriver(
	ctx context.Context,
	sess *session.Session,
	lc *loopConfig,
	out chan<- types.EngineEvent,
) subAgentLoopResult {
	ls := &loopState{}
	logger := lc.logger

	// L3 invariant: strip dispatch tools defensively. Even if a custom
	// AgentDefinition listed Task in AllowedTools (which Validate now
	// rejects, but stale stored definitions might still exist), the driver
	// refuses to expose them. Belt + suspenders.
	pool := lc.pool.WithoutNames(dispatchToolNames)

	// Sub-agent approval function auto-approves. L3 has no UI to ask;
	// permission decisions belong to the L1 main loop, not the leaf.
	approvalFn := func(_ context.Context, _ chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  true,
			Scope:     types.PermissionScopeOnce,
			Message:   "sub-agent auto-approved",
		}
	}
	executor := NewToolExecutor(pool, lc.permChecker, logger, lc.config.ToolTimeout, approvalFn)
	subProducer := tool.ArtifactProducer{
		AgentID:   lc.agentID,
		SessionID: sess.ID,
		TaskID:    lc.taskID,
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
	// AgentScope plumbing was previously only wired in runSubAgentLoop
	// (the Coordinator path). TierSubAgent dispatches land here in
	// runSubAgentDriver, which means L3 tools like meta_write / submit_task_result
	// saw an empty SessionRoot in ctx and rejected with "SessionRoot missing
	// in ctx — engine bug". Mirror the call here so the L3 driver gets the
	// same per-spawn scope as the coordinator path.
	executor.SetAgentScope(tool.AgentScope{
		ReadScope:   lc.readScope,
		WriteScope:  lc.writeScope,
		SessionRoot: lc.sessionRoot,
		TaskID:      lc.taskID,
		Agent:       lc.subagentType,
	})

	// L3 is contract-enforced unconditionally. Plain end_turn is never
	// terminal — the loop exits only when submit_task_result passes or
	// escalate_to_planner fires (or a hard cap trips).
	var (
		submitAccepted     bool
		submitArtifacts    []types.ArtifactRef
		submitSummary      string
		submitNudges       int
		submitRejects      int
		contractFailures   []string
		needsPlanning      bool
		escalationReason   string
		suggestedNextSteps string
	)

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, effectiveContextWindow(lc.config.ContextWindow), lc.config.AutoCompactThreshold) {
			logger.Info("sub-agent driver auto-compact triggered", zap.Int("msg_count", len(messages)))
			compacted, err := qe.compactor.Compact(ctx, messages)
			if err != nil {
				logger.Warn("sub-agent driver auto-compact failed", zap.Error(err))
			} else {
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		if ls.turn > lc.config.MaxTurns {
			return subAgentLoopResult{
				Terminal: types.Terminal{
					Reason:  types.TerminalMaxTurns,
					Message: fmt.Sprintf("sub-agent driver reached max turns (%d)", lc.config.MaxTurns),
					Turn:    ls.turn - 1,
				},
				ContractFailures: contractFailures,
			}
		}

		systemPrompt := lc.systemPromptOverride
		if systemPrompt == "" {
			systemPrompt = qe.buildSubAgentSystemPrompt(ctx, sess, messages, lc.profile, lc.subagentType, lc.allowedSkills, pool, lc.sessionRoot)
		}

		req := &provider.ChatRequest{
			Messages:      messages,
			System:        systemPrompt,
			Tools:         pool.Schemas(),
			MaxTokens:     lc.config.MaxTokens,
			ContextWindow: effectiveContextWindow(lc.config.ContextWindow),
		}
		if lc.temperature != nil {
			req.Temperature = *lc.temperature
		}

		logger.Debug("sub-agent driver LLM request",
			zap.Int("turn", ls.turn),
			zap.Int("message_count", len(messages)),
			zap.Int("tool_count", pool.Size()),
		)

		// ---- Phase 2: LLM Call with retry ----
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     qe.provider.Name(),
		}

		llmResult := callLLM(ctx, qe.provider, req, logger, qe.retryer, qe.llmTimeouts(), lc.agentID, out, out)

		if llmResult.streamErr != nil {
			llmErr := llmResult.streamErr
			out <- types.EngineEvent{Type: types.EngineEventError, Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "sub-agent driver cancelled", Turn: ls.turn}, ContractFailures: contractFailures}
			}
			logger.Error("sub-agent driver LLM call failed after retries", zap.Error(llmErr))
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalModelError, Message: llmErr.Error(), Turn: ls.turn}, ContractFailures: contractFailures}
		}

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

		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage, llmResult.reasoning)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): No tool calls ----
		// Plain end_turn is never terminal for L3 — the contract demands
		// either submit_task_result or escalate_to_planner.
		if len(toolCalls) == 0 {
			if submitAccepted {
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalCompleted,
						Message: "sub-agent finished with passing submission",
						Turn:    ls.turn,
					},
					SubmittedArtifacts: submitArtifacts,
					Summary:            submitSummary,
				}
			}
			if needsPlanning {
				// Should not happen — escalation exits immediately on
				// the tool-result path. Defensive fallback only.
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalCompleted,
						Message: "sub-agent escalated to planner",
						Turn:    ls.turn,
					},
					NeedsPlanning:      true,
					EscalationReason:   escalationReason,
					SuggestedNextSteps: suggestedNextSteps,
				}
			}
			// Nudge.
			submitNudges++
			if submitNudges > maxSubmitNudges {
				logger.Warn("sub-agent driver end_turn without submission, exceeded nudge cap",
					zap.Int("nudges", submitNudges),
				)
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalMaxTurns,
						Message: fmt.Sprintf("L3 declined to submit after %d reminders", maxSubmitNudges),
						Turn:    ls.turn,
					},
					ContractFailures: append(contractFailures,
						fmt.Sprintf("missing submit_task_result after %d nudges", maxSubmitNudges)),
				}
			}
			logger.Info("nudging sub-agent driver to submit",
				zap.Int("nudge", submitNudges),
				zap.Int("cap", maxSubmitNudges),
			)
			sess.AddMessage(buildDriverNudgeMessage(submitNudges, lc.expectedOutputs))
			continue
		}

		// ---- Phase 4: Tool execution ----
		if ctx.Err() != nil {
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent driver cancelled before tool execution", Turn: ls.turn}, ContractFailures: contractFailures}
		}

		execCtx := ctx
		if lc.allowedSkills != nil {
			execCtx = tool.WithAllowedSkills(execCtx, lc.allowedSkills)
		}
		if lc.skillTracker != nil {
			execCtx = tool.WithSkillTrackerValue(execCtx, lc.skillTracker)
		}

		results := qe.dispatchToolBatch(execCtx, executor, pool, toolCalls, out)

		if ctx.Err() != nil {
			return subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent driver cancelled during tool execution", Turn: ls.turn}, ContractFailures: contractFailures}
		}

		// Append tool results to session and inspect for terminal signals.
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

			// Inspect render_hint for L3 terminal signals.
			hint, _ := results[i].Metadata["render_hint"].(string)
			switch hint {
			case submittool.MetadataRenderHint:
				accepted, _ := results[i].Metadata[submittool.MetadataKeyAccepted].(bool)
				if accepted {
					// Local-files-as-truth model: submission no longer
					// carries an artifacts list (meta.json is the
					// contract). Accepted = meta.json read+validated by
					// the tool; no further self-check is needed at the
					// driver layer.
					submitAccepted = true
					submitArtifacts = nil
					// Pull meta.json's summary out of the submit tool's
					// metadata into loopResult.Summary. Without this the
					// upper Scheduler falls back to parseSummary(text)
					// and only sees the LLM's last thinking lines —
					// which is why ReviewGoal kept judging "outputs not
					// mentioned" and triggering a replan.
					submitSummary = strFromMeta(results[i].Metadata, "summary")
					logger.Info("sub-agent driver submission accepted",
						zap.String("task_id", strFromMeta(results[i].Metadata, "task_id")),
						zap.String("meta_path", strFromMeta(results[i].Metadata, "meta_path")),
					)
				} else {
					submitRejects++
					if reason, ok := results[i].Metadata["reason"].(string); ok {
						contractFailures = append(contractFailures, reason)
					}
					logger.Info("sub-agent driver submission rejected",
						zap.Int("reject_count", submitRejects),
						zap.Int("cap", maxSubmitRejects),
					)
					if submitRejects > maxSubmitRejects {
						return subAgentLoopResult{
							Terminal: types.Terminal{
								Reason:  types.TerminalMaxTurns,
								Message: fmt.Sprintf("submit_task_result rejected %d times — abandoning task", submitRejects),
								Turn:    ls.turn,
							},
							ContractFailures: contractFailures,
						}
					}
				}

			case submittool.EscalateMetadataRenderHint:
				// L3 self-reported impossibility. Exit immediately with the
				// reason so the parent can re-plan. Don't keep looping;
				// further turns can only generate noise after this signal.
				escalationReason, _ = results[i].Metadata["escalation_reason"].(string)
				suggestedNextSteps, _ = results[i].Metadata["suggested_next_steps"].(string)
				needsPlanning = true
				logger.Info("sub-agent driver escalated to planner",
					zap.String("reason", escalationReason),
					zap.String("suggested_next_steps", suggestedNextSteps),
				)
				return subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalCompleted,
						Message: "sub-agent escalated to planner",
						Turn:    ls.turn,
					},
					NeedsPlanning:      true,
					EscalationReason:   escalationReason,
					SuggestedNextSteps: suggestedNextSteps,
				}
			}
		}

		// M4 — emit NextRoundThinking so the channel layer can pre-open
		// a new message card with "正在解读结果" hint. Fires only when we're
		// actually doing another LLM round (tools were called).
		if len(toolCalls) > 0 {
			out <- types.EngineEvent{
				Type:    types.EngineEventNextRoundThinking,
				AgentID: lc.agentID,
			}
		}

		// ---- Phase 5 (part B): Continue loop ----
	}
}

// strFromMeta safely extracts a string from a metadata map. Used by the
// submission-accepted log line to surface task_id / meta_path without
// boilerplate type-assertion at each call site.
func strFromMeta(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// buildDriverNudgeMessage is the L3-specific reminder injected when the
// driver loop reaches end_turn without a terminal signal.
//
// Tone & length: kept tight on purpose (P0 优化, 2026-05-04). Earlier the
// 3 nudges were progressively wordier — same rule restated 3 times. That
// taught the model nothing it didn't see on nudge 1, just inflated the
// prompt by ~1 KB on a stuck loop. Now all 3 nudges share the same core
// sentence; only the final attempt appends a one-line escalation warning.
func buildDriverNudgeMessage(nudge int, outs []types.ExpectedOutput) types.Message {
	var b []byte
	b = append(b, fmt.Sprintf(
		"[SYSTEM] 任务未终止 (%d/%d)。必须二选一：调 submit_task_result 提交产物，或调 escalate_to_planner 说明做不了。",
		nudge, maxSubmitNudges)...)
	if nudge >= maxSubmitNudges {
		b = append(b, "\n（最后一次机会——再不调用即判失败。）"...)
	}
	if len(outs) > 0 {
		b = append(b, "\n必交 role: "...)
		first := true
		for _, o := range outs {
			if !o.Required {
				continue
			}
			if !first {
				b = append(b, " / "...)
			}
			b = append(b, o.Role...)
			first = false
		}
	}
	return types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText,
			Text: string(b),
		}},
		CreatedAt: time.Now(),
	}
}
