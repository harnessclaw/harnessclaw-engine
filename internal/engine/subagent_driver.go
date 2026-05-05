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
var dispatchToolNames = []string{"Task", "Specialists", "Orchestrate"}

// runSubAgentDriver is the dedicated L3 ReAct executor. It enforces every
// invariant the user's L3 design called out:
//
//   - No further dispatch — dispatchToolNames pre-stripped from the pool.
//   - Mandatory submission — TierSubAgent always has hasContract=true; the
//     loop refuses to terminate without SubmitTaskResult OR EscalateToPlanner.
//   - Stateless protocol — escalation goes via the EscalateToPlanner tool
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
// and the post-tool result inspection. Helpers retryLLMCall,
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
	if qe.artifactStore != nil {
		executor.SetArtifactStore(qe.artifactStore)
	}
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

	// L3 is contract-enforced unconditionally. Plain end_turn is never
	// terminal — the loop exits only when SubmitTaskResult passes or
	// EscalateToPlanner fires (or a hard cap trips).
	var (
		submitAccepted     bool
		submitArtifacts    []types.ArtifactRef
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

		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, lc.config.MaxTokens, lc.config.AutoCompactThreshold) {
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
			systemPrompt = qe.buildSubAgentSystemPrompt(ctx, sess, messages, lc.profile, lc.subagentType, lc.allowedSkills, pool)
		}

		req := &provider.ChatRequest{
			Messages:  messages,
			System:    systemPrompt,
			Tools:     pool.Schemas(),
			MaxTokens: lc.config.MaxTokens,
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

		llmResult := retryLLMCall(ctx, qe.provider, req, logger, out)

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

		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): No tool calls ----
		// Plain end_turn is never terminal for L3 — the contract demands
		// either SubmitTaskResult or EscalateToPlanner.
		if len(toolCalls) == 0 {
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
						fmt.Sprintf("missing SubmitTaskResult after %d nudges", maxSubmitNudges)),
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
					// Pull refs first — self-check needs them.
					var refs []types.ArtifactRef
					if got, ok := results[i].Metadata[submittool.MetadataKeyArtifacts].([]types.ArtifactRef); ok {
						refs = got
					}
					// Run the post-submit self-check (P0-2). Catches
					// SEMANTIC mismatches that M4 doesn't (e.g. role
					// not in def's declared role list, zero-size after
					// store round-trip). On failure we treat the
					// submission like a rejection and force a retry —
					// distinct from M4 reject so contract_failures gets
					// a 'self_check' prefix the LLM can spot.
					if checkFails := selfCheckSubmission(lc, refs); len(checkFails) > 0 {
						submitRejects++
						for _, f := range checkFails {
							contractFailures = append(contractFailures, "self_check: "+f)
						}
						logger.Info("sub-agent driver submission failed self-check",
							zap.Int("reject_count", submitRejects),
							zap.Strings("failures", checkFails),
						)
						if submitRejects > maxSubmitRejects {
							return subAgentLoopResult{
								Terminal: types.Terminal{
									Reason:  types.TerminalMaxTurns,
									Message: fmt.Sprintf("self-check rejected submission %d times — abandoning task", submitRejects),
									Turn:    ls.turn,
								},
								ContractFailures:  contractFailures,
								SelfCheckFailures: checkFails,
							}
						}
						sess.AddMessage(buildSelfCheckNudge(checkFails))
						continue
					}
					submitAccepted = true
					submitArtifacts = refs
					logger.Info("sub-agent driver submission accepted",
						zap.Int("artifacts", len(submitArtifacts)),
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
								Message: fmt.Sprintf("SubmitTaskResult rejected %d times — abandoning task", submitRejects),
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

		// ---- Phase 5 (part B): Continue loop ----
	}
}

// selfCheckSubmission runs the post-submit semantic check (P0-2 from your
// L3 gap list). M4 already validates store-side properties (lineage, size,
// role-exists-in-contract); self-check catches SEMANTIC mismatches that
// look fine to the store but break the agent's declared contract. Today
// it enforces:
//
//   - Every submitted artifact has a non-empty role string. M4 only
//     checks role membership when ExpectedOutputs is non-empty; for
//     no-contract submissions a blank role would otherwise sneak through.
//   - Each artifact's SizeBytes > 0. The store checks size on write but
//     a buggy upstream can synthesize a zero-byte ref; defense in depth.
//
// Returns nil when everything passes. Failures are short, actionable
// strings the LLM can read in the next turn.
//
// Future expansions (don't add yet — keep it tight):
//   - cross-check artifact mime_type against an OutputSchema enum
//   - require source-citation count when role implies it
//   - per-agent custom selfCheck function passed via loopConfig
func selfCheckSubmission(lc *loopConfig, refs []types.ArtifactRef) []string {
	if len(refs) == 0 {
		return []string{"submission carries zero artifacts"}
	}
	var fails []string
	for i, r := range refs {
		if r.Role == "" {
			fails = append(fails, fmt.Sprintf("artifacts[%d] (id=%s) is missing a role", i, r.ArtifactID))
		}
		if r.SizeBytes <= 0 {
			fails = append(fails, fmt.Sprintf("artifacts[%d] (role=%s, id=%s) is empty (size 0)", i, r.Role, r.ArtifactID))
		}
	}
	return fails
}

// buildSelfCheckNudge composes the SYSTEM message appended after a
// self-check failure so the LLM gets a concrete fix-and-resubmit
// directive on the next turn. Distinct from buildDriverNudgeMessage
// (which fires on plain end_turn without any submit attempt).
func buildSelfCheckNudge(failures []string) types.Message {
	var b []byte
	b = append(b, "[SYSTEM] SubmitTaskResult 通过 store 校验，但 sub-agent 自检失败。修正下面的问题后再调一次：\n"...)
	for _, f := range failures {
		b = append(b, "- "...)
		b = append(b, f...)
		b = append(b, '\n')
	}
	b = append(b, "重写有问题的 artifact，再调 SubmitTaskResult 提交（同 ID 不行就用 parent_artifact_id 产新版本）。\n"...)
	return types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText,
			Text: string(b),
		}},
		CreatedAt: time.Now(),
	}
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
		"[SYSTEM] 任务未终止 (%d/%d)。必须二选一：调 SubmitTaskResult 提交产物，或调 EscalateToPlanner 说明做不了。",
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
