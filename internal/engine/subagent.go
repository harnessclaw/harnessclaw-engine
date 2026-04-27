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
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: cfg.Prompt,
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
				Text: distillPrompt,
			}},
			CreatedAt: time.Now(),
		})
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
	// First filter by AgentType (coarse), then by AgentDefinition.AllowedTools (fine).
	pool := tool.NewToolPool(qe.registry, nil, nil)
	pool = pool.FilteredFor(cfg.AgentType)

	// Look up agent definition for tool/skill/profile customization.
	var agentDef *agent.AgentDefinition
	if qe.defRegistry != nil && cfg.SubagentType != "" {
		agentDef = qe.defRegistry.Get(cfg.SubagentType)
	}

	if agentDef != nil && len(agentDef.AllowedTools) > 0 {
		pool = pool.FilterByNames(agentDef.AllowedTools)
		logger.Debug("tool pool filtered by agent definition",
			zap.String("agent", cfg.SubagentType),
			zap.Int("tools_after_filter", pool.Size()),
			zap.Strings("allowed", agentDef.AllowedTools),
		)
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

	lc := &loopConfig{
		pool:                 pool,
		profile:              profile,
		permChecker:          permChecker,
		config:               subConfig,
		systemPromptOverride: systemPromptOverride,
		subagentType:         cfg.SubagentType,
		allowedSkills:        allowedSkills,
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
	var deliverables []types.Deliverable

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

	// Step 14: Return SpawnResult with structured fields.
	// - Output: full text (stored in TaskRegistry for reference)
	// - Summary: extracted from <summary> tag (returned to emma in tool_result)
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

	// Build the output that emma sees in tool_result:
	// Only summary + deliverable list, NOT the full output.
	var emmaOutput strings.Builder
	emmaOutput.WriteString(summary)
	if len(deliverables) > 0 {
		emmaOutput.WriteString("\n\n产出文件：\n")
		for _, d := range deliverables {
			emmaOutput.WriteString(fmt.Sprintf("- %s（%s，%d 字节）\n", d.FilePath, d.Language, d.ByteSize))
		}
	}

	spawnResult := &agent.SpawnResult{
		Output:       emmaOutput.String(),
		Summary:      summary,
		Status:       status,
		Attempts:     1, // TODO: increment when task-level retry is implemented
		Deliverables: deliverables,
		Terminal:     &terminal,
		Usage:        &cumulativeUsage,
		SessionID:    sessionID,
		AgentID:      agentID,
		NumTurns:     terminal.Turn,
	}

	// Record full result in TaskRegistry for future reference (context passing, debugging).
	// Store the full output separately — emma only sees the summary.
	fullResult := *spawnResult
	fullResult.Output = fullOutput // preserve full sub-agent output
	qe.taskRegistryMu.Lock()
	qe.taskRegistry[agentID] = &fullResult
	qe.taskRegistryMu.Unlock()

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
			systemPrompt = qe.buildSubAgentSystemPrompt(ctx, sess, messages, lc.profile, lc.subagentType, lc.allowedSkills)
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

		// Inject allowed skills into context so SkillTool can enforce the whitelist.
		execCtx := ctx
		if lc.allowedSkills != nil {
			execCtx = tool.WithAllowedSkills(execCtx, lc.allowedSkills)
		}

		results := executor.ExecuteBatch(execCtx, toolCalls, out)

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
// subagentType is the agent definition name (e.g., "developer", "researcher")
// used to look up the worker's identity from the definition registry.
// When allowedSkills is non-nil, only the listed skills appear in the prompt.
func (qe *QueryEngine) buildSubAgentSystemPrompt(
	_ context.Context,
	sess *session.Session,
	messages []types.Message,
	profile *prompt.AgentProfile,
	subagentType string,
	allowedSkills map[string]bool,
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
	var workerIdentity string
	var def *agent.AgentDefinition
	if qe.defRegistry != nil && subagentType != "" {
		def = qe.defRegistry.Get(subagentType)
	}
	if def != nil {
		if def.SystemPrompt != "" {
			// Use custom system prompt if set (e.g., from YAML).
			workerIdentity = def.SystemPrompt
		} else if def.DisplayName != "" {
			// Auto-generate identity from definition metadata.
			var identity strings.Builder
			identity.WriteString(fmt.Sprintf("你叫%s，是 emma 团队的搭档。\n", def.DisplayName))
			if def.Description != "" {
				identity.WriteString(fmt.Sprintf("你的专长：%s。\n", def.Description))
			}
			if def.Personality != "" {
				identity.WriteString(fmt.Sprintf("你的风格：%s。\n", def.Personality))
			}
			identity.WriteString("\nemma 派你来执行一项具体任务，请专注完成。")
			workerIdentity = identity.String()
		}
	}

	promptCtx := &prompt.PromptContext{
		SessionID:            sess.ID,
		Turn:                 len(messages),
		Session:              sess,
		Tools:                qe.registry,
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
