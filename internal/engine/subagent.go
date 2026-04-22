package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
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

	// Step 5: Initialize conversation context.
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
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: cfg.Prompt,
			}},
			CreatedAt: time.Now(),
		})
		systemPromptOverride = cfg.SystemPromptOverride
	} else {
		// Spawn mode: blank session with just the prompt.
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: cfg.Prompt,
			}},
			CreatedAt: time.Now(),
		})
	}

	// Step 6: Build filtered ToolPool.
	pool := tool.NewToolPool(qe.registry, nil, nil)
	pool = pool.FilteredFor(cfg.AgentType)

	// Step 7: Resolve prompt profile (skip for fork mode with override).
	profile := resolveSubAgentProfile(cfg.SubagentType)

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

	lc := &loopConfig{
		pool:                 pool,
		profile:              profile,
		permChecker:          permChecker,
		config:               subConfig,
		systemPromptOverride: systemPromptOverride,
		logger:               logger,
	}

	// Step 10: Emit subagent.start event.
	if cfg.ParentOut != nil {
		cfg.ParentOut <- types.EngineEvent{
			Type:          types.EngineEventSubAgentStart,
			AgentID:       agentID,
			AgentName:     cfg.Name,
			AgentDesc:     cfg.Description,
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
	var terminal types.Terminal
	var textBuf strings.Builder
	var cumulativeUsage types.Usage

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		terminal = qe.runSubAgentLoop(ctx, sess, lc, out)
	}()

	for evt := range out {
		switch evt.Type {
		case types.EngineEventText:
			textBuf.WriteString(evt.Text)
		case types.EngineEventDone:
			if evt.Usage != nil {
				cumulativeUsage = *evt.Usage
			}
		}

		// Forward events to parent's output channel for real-time streaming.
		// Wrap in EngineEventSubAgentEvent so they don't interfere with
		// the parent's message lifecycle in the EventMapper.
		if cfg.ParentOut != nil {
			var inner *types.SubAgentEventData
			switch evt.Type {
			case types.EngineEventText:
				inner = &types.SubAgentEventData{
					EventType: "text",
					Text:      evt.Text,
				}
			case types.EngineEventToolStart:
				inner = &types.SubAgentEventData{
					EventType: "tool_start",
					ToolName:  evt.ToolName,
					ToolUseID: evt.ToolUseID,
					ToolInput: evt.ToolInput,
				}
			case types.EngineEventToolEnd:
				inner = &types.SubAgentEventData{
					EventType: "tool_end",
					ToolName:  evt.ToolName,
					ToolUseID: evt.ToolUseID,
				}
				if evt.ToolResult != nil {
					inner.Output = evt.ToolResult.Content
					inner.IsError = evt.ToolResult.IsError
				}
			}
			if inner != nil {
				cfg.ParentOut <- types.EngineEvent{
					Type:          types.EngineEventSubAgentEvent,
					AgentID:       agentID,
					AgentName:     cfg.Name,
					SubAgentEvent: inner,
				}
			}
		}
	}
	<-done

	elapsed := time.Since(startTime)

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

	logger.Info("sub-agent completed",
		zap.String("terminal_reason", string(terminal.Reason)),
		zap.Int("turns", terminal.Turn),
		zap.Duration("elapsed", elapsed),
	)

	// Step 14: Return SpawnResult.
	return &agent.SpawnResult{
		Output:    textBuf.String(),
		Terminal:  &terminal,
		Usage:     &cumulativeUsage,
		SessionID: sessionID,
		AgentID:   agentID,
		NumTurns:  terminal.Turn,
	}, nil
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
	logger               *zap.Logger
}

// runSubAgentLoop is a variant of runQueryLoop parameterized by loopConfig.
// It uses the provided pool, profile, and permission checker instead of
// the engine's defaults.
func (qe *QueryEngine) runSubAgentLoop(
	ctx context.Context,
	sess *session.Session,
	lc *loopConfig,
	out chan<- types.EngineEvent,
) types.Terminal {
	ls := &loopState{}
	logger := lc.logger

	// Sub-agents get their own artifact store (independent of parent).
	artStore := artifact.NewStore()
	artRS := artifact.NewReplacementState()

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
	executor.SetArtifactStore(artifact.AsToolStore(artStore))

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

		// Apply artifact references for sub-agent context.
		messages = artifact.CompactMessages(messages, artRS)

		// Check max turns.
		if ls.turn > lc.config.MaxTurns {
			return types.Terminal{
				Reason:  types.TerminalMaxTurns,
				Message: fmt.Sprintf("sub-agent reached max turns (%d)", lc.config.MaxTurns),
				Turn:    ls.turn - 1,
			}
		}

		// Build system prompt.
		systemPrompt := lc.systemPromptOverride
		if systemPrompt == "" {
			systemPrompt = qe.buildSubAgentSystemPrompt(ctx, sess, messages, lc.profile)
		}

		req := &provider.ChatRequest{
			Messages:  messages,
			System:    systemPrompt,
			Tools:     lc.pool.Schemas(),
			MaxTokens: lc.config.MaxTokens,
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
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "sub-agent cancelled", Turn: ls.turn}
			}
			logger.Error("sub-agent LLM call failed after retries", zap.Error(llmErr))
			return types.Terminal{Reason: types.TerminalModelError, Message: llmErr.Error(), Turn: ls.turn}
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

		// ---- Phase 5 (part A): No tool calls = done ----
		if len(toolCalls) == 0 {
			return types.Terminal{
				Reason:  types.TerminalCompleted,
				Message: "sub-agent finished",
				Turn:    ls.turn,
			}
		}

		// ---- Phase 4: Server-side tool execution ----
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent cancelled before tool execution", Turn: ls.turn}
		}

		results := executor.ExecuteBatch(ctx, toolCalls, out)

		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "sub-agent cancelled during tool execution", Turn: ls.turn}
		}

		// Append tool results to session.
		for i, tc := range toolCalls {
			content := results[i].Content
			var artID string

			// Persist large tool results as artifacts.
			content, artID = artifact.PersistAndReplace(
				artStore, artRS,
				tc.ID, tc.Name,
				content, results[i].IsError,
				results[i].Metadata,
				artifact.DefaultThreshold,
				artifact.DefaultPreviewLen,
			)

			toolMsg := types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type:       types.ContentTypeToolResult,
					ToolUseID:  tc.ID,
					ToolName:   tc.Name,
					ToolResult: content,
					IsError:    results[i].IsError,
					ArtifactID: artID,
				}},
				CreatedAt: time.Now(),
			}
			sess.AddMessage(toolMsg)

			for _, nm := range results[i].NewMessages {
				sess.AddMessage(nm)
			}
		}

		// ---- Phase 5 (part B): Continue loop ----
	}
}

// buildSubAgentSystemPrompt builds a system prompt for the sub-agent using
// the given profile, without touching the parent's prompt cache.
func (qe *QueryEngine) buildSubAgentSystemPrompt(
	_ context.Context,
	sess *session.Session,
	messages []types.Message,
	profile *prompt.AgentProfile,
) string {
	if qe.promptBuilder == nil {
		return qe.config.SystemPrompt
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	promptCtx := &prompt.PromptContext{
		SessionID:         sess.ID,
		Turn:              len(messages),
		Session:           sess,
		Tools:             qe.registry,
		TotalTokensUsed:   totalTokens,
		ContextWindowSize: 200000,
		Memory:            make(map[string]string),
		EnvInfo:           qe.getEnvSnapshot(),
		SkillListing:      qe.getSkillListing(),
	}

	output, err := qe.promptBuilder.Build(promptCtx, profile)
	if err != nil {
		qe.logger.Warn("sub-agent prompt build failed, using fallback", zap.Error(err))
		return qe.config.SystemPrompt
	}

	return output.ToSystemPrompt()
}

// resolveSubAgentProfile maps a subagent_type string to a prompt profile.
func resolveSubAgentProfile(subagentType string) *prompt.AgentProfile {
	return prompt.ResolveProfileBySubagentType(subagentType)
}
