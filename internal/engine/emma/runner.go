package emma

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/llmcall"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/toolexec"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// loopState tracks mutable state across turns of the query loop.
type loopState struct {
	turn            int
	stopReason      string
	lastUsage       *types.Usage
	cumulativeUsage types.Usage
}

// run implements the 5-phase query loop modeled on TypeScript query.ts.
//
// Phase 1: Preprocess — assemble system prompt, tool schemas, compact if needed.
// Phase 2: LLM Call — stream the response, collect text + tool calls.
// Phase 3: Error Recovery — handle prompt_too_long, max_output_tokens, rate limits.
// Phase 4: Tool Execution — run requested tools (server-side or client-side).
// Phase 5: Continuation — check terminal conditions, append assistant + tool messages, loop.
func (e *Engine) run(
	ctx context.Context,
	sess *session.Session,
	out chan<- types.EngineEvent,
	approvalFn toolexec.PermissionApprovalFunc,
) types.Terminal {
	ls := &loopState{}
	cfg := e.config
	logger := e.logger
	prov := e.provider

	// Register a mailbox so async sub-agents can send completion
	// notifications back. Deregister on exit.
	var mailbox *agent.Mailbox
	if broker := e.messageBroker; broker != nil {
		mailbox = broker.Register(sess.ID, "")
		defer broker.Unregister(sess.ID)
	}

	pool := tool.NewToolPool(e.registry, nil, nil)
	if len(cfg.MainAgentAllowedTools) > 0 {
		pool = pool.FilterByNames(cfg.MainAgentAllowedTools)
	}

	executor := toolexec.NewToolExecutor(pool, e.permChecker, logger, cfg.ToolTimeout, approvalFn)
	if e.statsRegistry != nil {
		executor.SetStatsRegistry(e.statsRegistry)
	}
	producer := tool.ArtifactProducer{
		AgentID:   "main",
		SessionID: sess.ID,
	}
	if tc := emit.FromContext(ctx); tc != nil {
		producer.TraceID = tc.TraceID
	}
	executor.SetArtifactProducer(producer)

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		if e.compactor != nil && e.compactor.ShouldCompact(messages, e.contextWindow(), cfg.AutoCompactThreshold) {
			logger.Info("auto-compact triggered", zap.String("session_id", sess.ID), zap.Int("msg_count", len(messages)))

			compacted, err := e.compactor.Compact(ctx, messages)
			if err != nil {
				logger.Warn("auto-compact failed, continuing with full history", zap.Error(err))
			} else {
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		// Honour MainAgentMaxTurns when set; sub-agents use MaxTurns
		// directly via runSubAgentDriver.
		mainMax := cfg.MaxTurns
		if cfg.MainAgentMaxTurns > 0 {
			mainMax = cfg.MainAgentMaxTurns
		}
		if ls.turn > mainMax {
			return types.Terminal{
				Reason:  types.TerminalMaxTurns,
				Message: fmt.Sprintf("reached max turns (%d)", mainMax),
				Turn:    ls.turn - 1,
			}
		}

		systemPrompt := e.buildSystemPrompt(ctx, sess, messages)

		req := &provider.ChatRequest{
			Messages:      messages,
			System:        systemPrompt,
			Tools:         pool.Schemas(),
			MaxTokens:     cfg.MaxTokens,
			ContextWindow: e.contextWindow(),
		}

		logger.Debug("========== LLM REQUEST DUMP START ==========",
			zap.String("session_id", sess.ID),
			zap.Int("turn", ls.turn),
			zap.Int("message_count", len(messages)),
			zap.Int("tool_schema_count", len(pool.Schemas())),
			zap.Int("system_prompt_len", len(systemPrompt)),
			zap.Int("max_tokens", cfg.MaxTokens),
		)
		for i, m := range messages {
			contentPreview := ""
			for _, cb := range m.Content {
				if cb.Type == types.ContentTypeText && len(cb.Text) > 0 {
					preview := cb.Text
					if len(preview) > 200 {
						preview = preview[:200] + "...[truncated]"
					}
					contentPreview = preview
					break
				}
				if cb.Type == types.ContentTypeToolUse {
					contentPreview = fmt.Sprintf("[tool_use: %s]", cb.ToolName)
					break
				}
				if cb.Type == types.ContentTypeToolResult {
					preview := cb.ToolResult
					if len(preview) > 100 {
						preview = preview[:100] + "...[truncated]"
					}
					contentPreview = fmt.Sprintf("[tool_result: %s] %s", cb.ToolName, preview)
					break
				}
			}
			logger.Debug("llm request message",
				zap.Int("index", i),
				zap.String("role", string(m.Role)),
				zap.Int("content_blocks", len(m.Content)),
				zap.Int("tokens", m.Tokens),
				zap.String("preview", contentPreview),
			)
		}
		logger.Debug("========== LLM REQUEST DUMP END ==========")

		// ---- Phase 2: LLM Call (streaming) ----
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     prov.Name(),
			Usage: &types.Usage{
				InputTokens: ls.cumulativeUsage.InputTokens,
			},
		}

		timeouts := llmcall.LLMTimeouts(cfg.LLMAPITimeout, cfg.LLMFirstByteTimeout)
		llmResult := llmcall.CallLLM(ctx, prov, req, logger, e.retryer, timeouts, "main", out, out)

		if llmResult.StreamErr != nil {
			llmErr := llmResult.StreamErr
			out <- types.EngineEvent{Type: types.EngineEventError, Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "query cancelled", Turn: ls.turn}
			}
			logger.Error("LLM call failed after retries",
				zap.String("session_id", sess.ID),
				zap.Int("turn", ls.turn),
				zap.Error(llmErr),
			)
			return types.Terminal{Reason: types.TerminalModelError, Message: llmErr.Error(), Turn: ls.turn}
		}

		textBuf := llmResult.TextBuf
		toolCalls := llmResult.ToolCalls

		ls.stopReason = llmResult.StopReason
		if llmResult.LastUsage != nil {
			ls.lastUsage = llmResult.LastUsage
			ls.cumulativeUsage.InputTokens += llmResult.LastUsage.InputTokens
			ls.cumulativeUsage.OutputTokens += llmResult.LastUsage.OutputTokens
			ls.cumulativeUsage.CacheRead += llmResult.LastUsage.CacheRead
			ls.cumulativeUsage.CacheWrite += llmResult.LastUsage.CacheWrite
		}

		stopReason := ls.stopReason
		if stopReason == "" {
			if len(toolCalls) > 0 {
				stopReason = "tool_use"
			} else {
				stopReason = "end_turn"
			}
		}
		out <- types.EngineEvent{
			Type:       types.EngineEventMessageDelta,
			StopReason: stopReason,
			Usage:      ls.lastUsage,
		}

		out <- types.EngineEvent{Type: types.EngineEventMessageStop}

		assistantMsg := BuildAssistantMessage(textBuf, toolCalls, ls.lastUsage, llmResult.Reasoning)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): no tool calls means LLM is done. ----
		if len(toolCalls) == 0 {
			if e.shouldWaitForAsyncAgents(sess.ID, mailbox) {
				if msg := e.waitForMailboxMessage(ctx, sess.ID, mailbox, out); msg != nil {
					sess.AddMessage(*msg)
					continue
				}
			}
			return types.Terminal{
				Reason:  types.TerminalCompleted,
				Message: "model finished",
				Turn:    ls.turn,
			}
		}

		// ---- Phase 4: Tool Execution ----
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled before tool execution", Turn: ls.turn}
		}

		results := e.dispatchToolBatch(ctx, sess, executor, pool, toolCalls, out)

		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled during tool execution", Turn: ls.turn}
		}

		for i, tc := range toolCalls {
			toolMsg := types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{
						Type:       types.ContentTypeToolResult,
						ToolUseID:  tc.ID,
						ToolName:   tc.Name,
						ToolResult: results[i].Content,
						IsError:    results[i].IsError,
					},
				},
				CreatedAt: time.Now(),
			}
			sess.AddMessage(toolMsg)

			for _, nm := range results[i].NewMessages {
				sess.AddMessage(nm)
			}
		}

		// ---- Phase 5 (part B): Check stop reason. ----
		if ls.stopReason == "end_turn" && len(toolCalls) > 0 {
			continue
		}

		if len(toolCalls) > 0 {
			out <- types.EngineEvent{
				Type:    types.EngineEventNextRoundThinking,
				AgentID: "", // main loop
			}
		}
	}
}

// dispatchToolBatch routes each tool call to either the client (via
// tool.call) or the server-side executor based on per-tool policy.
func (e *Engine) dispatchToolBatch(
	ctx context.Context,
	sess *session.Session,
	executor *toolexec.ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	if len(toolCalls) == 0 {
		return nil
	}

	results := make([]types.ToolResult, len(toolCalls))

	var clientCalls, serverCalls []types.ToolCall
	var clientIdx, serverIdx []int
	for i, tc := range toolCalls {
		if e.routeToClient(pool, tc.Name) {
			clientCalls = append(clientCalls, tc)
			clientIdx = append(clientIdx, i)
		} else {
			serverCalls = append(serverCalls, tc)
			serverIdx = append(serverIdx, i)
		}
	}

	if len(serverCalls) > 0 {
		serverResults := executor.ExecuteBatch(ctx, serverCalls, out)
		for j, r := range serverResults {
			results[serverIdx[j]] = r
		}
	}

	if len(clientCalls) > 0 {
		clientResults := e.executeClientTools(ctx, sess, pool, clientCalls, out)
		for j, r := range clientResults {
			results[clientIdx[j]] = r
		}
	}

	return results
}

// routeToClient decides whether a tool call should be sent to the
// connected client (true) or executed server-side (false).
func (e *Engine) routeToClient(pool *tool.ToolPool, toolName string) bool {
	if t := pool.Get(toolName); t != nil {
		if cr, ok := t.(tool.ClientRoutedTool); ok && cr.IsClientRouted() {
			return true
		}
	}
	return e.config.ClientTools
}

// executeClientTools sends tool.call events to the client and waits for
// tool.result responses.
func (e *Engine) executeClientTools(
	ctx context.Context,
	sess *session.Session,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	results := make([]types.ToolResult, len(toolCalls))

	awaits := make([]*session.ToolAwait, len(toolCalls))
	humanInteractive := make([]bool, len(toolCalls))
	for i, tc := range toolCalls {
		awaits[i] = sess.Awaits.PushTool(tc.ID, tc.Name)

		if t := pool.Get(tc.Name); t != nil {
			if cr, ok := t.(tool.ClientRoutedTool); ok && cr.IsClientRouted() {
				humanInteractive[i] = true
			}
		}

		out <- types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		}
	}

	toolTimeout := e.config.ToolTimeout
	for i, tc := range toolCalls {
		if humanInteractive[i] {
			select {
			case <-ctx.Done():
				results[i] = types.ToolResult{
					Content: "execution cancelled",
					IsError: true,
				}
				sess.Awaits.ForgetTool(tc.ID)
			case payload := <-awaits[i].Result:
				results[i] = ToolResultFromPayload(payload)
			}
		} else {
			select {
			case <-ctx.Done():
				results[i] = types.ToolResult{
					Content: "execution cancelled",
					IsError: true,
				}
				sess.Awaits.ForgetTool(tc.ID)
			case payload := <-awaits[i].Result:
				results[i] = ToolResultFromPayload(payload)
			case <-time.After(toolTimeout):
				results[i] = types.ToolResult{
					Content: fmt.Sprintf("tool %s timed out waiting for client result", tc.Name),
					IsError: true,
				}
				sess.Awaits.ForgetTool(tc.ID)
			}
		}
	}

	return results
}

// shouldWaitForAsyncAgents returns true if the query loop should block on
// the mailbox instead of terminating.
func (e *Engine) shouldWaitForAsyncAgents(sessionID string, mailbox *agent.Mailbox) bool {
	if mailbox == nil {
		return false
	}
	if e.agentRegistry == nil {
		return false
	}
	return e.agentRegistry.HasRunningForParent(sessionID)
}

// waitForMailboxMessage blocks until a message arrives on the mailbox or
// the context is cancelled.
func (e *Engine) waitForMailboxMessage(
	ctx context.Context,
	sessionID string,
	mailbox *agent.Mailbox,
	out chan<- types.EngineEvent,
) *types.Message {
	_ = out
	if mailbox == nil {
		return nil
	}

	e.logger.Info("waiting for async agent notifications",
		zap.String("session_id", sessionID),
	)

	select {
	case <-ctx.Done():
		return nil
	case agentMsg, ok := <-mailbox.Receive():
		if !ok || agentMsg == nil {
			return nil
		}
		e.logger.Info("received async agent notification",
			zap.String("session_id", sessionID),
			zap.String("from", agentMsg.From),
			zap.String("type", string(agentMsg.Type)),
		)

		text := fmt.Sprintf("[Agent notification from %s]\n%s", agentMsg.From, agentMsg.Content)
		msg := &types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: text,
			}},
			CreatedAt: time.Now(),
		}
		return msg
	}
}

// cumulativeUsageFor returns the running token totals for sessionID by
// reading the sessionstats Tracker. Falls back to a zero Usage when stats
// are not wired in (tests).
func (e *Engine) cumulativeUsageFor(sessionID string) types.Usage {
	if e == nil {
		return types.Usage{}
	}
	if e.statsRegistry == nil {
		return types.Usage{}
	}
	tr := e.statsRegistry.Get(sessionID)
	if tr == nil {
		return types.Usage{}
	}
	s := tr.Snapshot()
	return types.Usage{
		InputTokens:    int(s.InputTokens),
		OutputTokens:   int(s.OutputTokens),
		CacheRead:      int(s.CacheReadTokens),
		CacheWrite:     int(s.CacheWriteTokens),
		ThinkingTokens: int(s.ThinkingTokens),
	}
}

