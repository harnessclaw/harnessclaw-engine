package queryloop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/llmcall"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/toolexec"
	"harnessclaw-go/internal/event"
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

// Run implements the 5-phase query loop modeled on TypeScript query.ts.
//
// Phase 1: Preprocess — assemble system prompt, tool schemas, compact if needed.
// Phase 2: LLM Call — stream the response, collect text + tool calls.
// Phase 3: Error Recovery — handle prompt_too_long, max_output_tokens, rate limits.
// Phase 4: Tool Execution — run requested tools (server-side or client-side).
// Phase 5: Continuation — check terminal conditions, append assistant + tool messages, loop.
//
// approvalFn is supplied by the engine so the loop can request permission
// approvals without coupling queryloop to the engine-side session lookup.
func (r *Runner) Run(
	ctx context.Context,
	sess *session.Session,
	out chan<- types.EngineEvent,
	approvalFn toolexec.PermissionApprovalFunc,
) types.Terminal {
	ls := &loopState{}
	cfg := r.deps.LoopConfig()
	logger := r.deps.Logger()
	prov := r.deps.Provider()

	// Register a mailbox for this session so async sub-agents can send
	// completion notifications back. Deregister on exit.
	var mailbox *agent.Mailbox
	if broker := r.deps.MessageBroker(); broker != nil {
		mailbox = broker.Register(sess.ID, "")
		defer broker.Unregister(sess.ID)
	}

	// Build the ToolPool once per query loop from the registry.
	pool := tool.NewToolPool(r.deps.Registry(), nil /*mcpTools*/, nil /*denyRules*/)

	// Restrict the main-agent tool palette when configured. L1Engine uses
	// this to expose only delegation tools (Agent, orchestrate) — sub-agents
	// keep the full palette via the SpawnSync path, which builds its own
	// pool independently in subagent.go.
	if len(cfg.MainAgentAllowedTools) > 0 {
		pool = pool.FilterByNames(cfg.MainAgentAllowedTools)
	}

	executor := toolexec.NewToolExecutor(pool, r.deps.PermChecker(), logger, cfg.ToolTimeout, approvalFn)
	if statsReg := r.deps.StatsRegistry(); statsReg != nil {
		executor.SetStatsRegistry(statsReg)
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

		// Auto-compact if needed.
		if compactor := r.deps.Compactor(); compactor != nil && compactor.ShouldCompact(messages, r.deps.ContextWindow(), cfg.AutoCompactThreshold) {
			logger.Info("auto-compact triggered", zap.String("session_id", sess.ID), zap.Int("msg_count", len(messages)))
			r.deps.EventBus().Publish(event.Event{Topic: event.TopicCompactTriggered, Payload: map[string]string{"session_id": sess.ID}})

			compacted, err := compactor.Compact(ctx, messages)
			if err != nil {
				logger.Warn("auto-compact failed, continuing with full history", zap.Error(err))
			} else {
				// Replace session messages with compacted history.
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		// Check max turns. The main-agent loop honours MainAgentMaxTurns
		// when set (L1Engine uses it to enforce a small L1 loop); sub-agents
		// use MaxTurns directly via runSubAgentDriver.
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

		// Build the LLM request.
		// Build structured system prompt using prompt builder.
		// Skill listing is now injected as a system prompt section (Layer 2),
		// replacing the previous per-turn <system-reminder> user message (Layer 3).
		systemPrompt := r.BuildSystemPrompt(ctx, sess, messages)

		req := &provider.ChatRequest{
			Messages:      messages,
			System:        systemPrompt,
			Tools:         pool.Schemas(),
			MaxTokens:     cfg.MaxTokens,
			ContextWindow: r.deps.ContextWindow(),
		}

		// --- LLM request observability: dump what we're sending to the model ---
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

		// Emit message.start before streaming begins.
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     prov.Name(),
			Usage: &types.Usage{
				InputTokens: ls.cumulativeUsage.InputTokens,
			},
		}

		// ---- Phase 2: LLM Call with retry ----
		// L1 main loop's agent identity is "main" — matches the
		// envelope.agent_id stamp on every emitter the main session
		// produces, so heartbeats route to the active turn / message
		// card.
		timeouts := llmcall.LLMTimeouts(cfg.LLMAPITimeout, cfg.LLMFirstByteTimeout)
		llmResult := llmcall.CallLLM(ctx, prov, req, logger, r.deps.Retryer(), timeouts, "main", out, out)

		if llmResult.StreamErr != nil {
			// ---- Phase 3: Error Recovery (all retries exhausted) ----
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

		// Events were already streamed in real-time by CallLLM.
		// Extract collected results for session state.
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

		// Emit message.delta with stop_reason and output usage.
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

		// Emit message.stop to close this message.
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}

		// Append assistant message to session. Reasoning is threaded
		// through so providers that require thinking-mode replay
		// (DeepSeek) see it on the next request.
		assistantMsg := BuildAssistantMessage(textBuf, toolCalls, ls.lastUsage, llmResult.Reasoning)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): Check terminal — no tool calls means LLM is done. ----
		if len(toolCalls) == 0 {
			// Before terminating, check if there are async sub-agents still
			// running for this session. If so, wait for their completion
			// notifications via the mailbox, inject them as user messages,
			// and continue the loop so the LLM can process the results.
			if r.shouldWaitForAsyncAgents(sess.ID, mailbox) {
				if msg := r.waitForMailboxMessage(ctx, sess.ID, mailbox, out); msg != nil {
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

		// Per-tool routing: split the batch into client-routed and
		// server-routed groups. A tool that implements ClientRoutedTool
		// (e.g. ask_user_question) ALWAYS goes to the client. Everything
		// else follows the global ClientTools flag — true means
		// delegate-everything (Claude Code CLI mode), false means
		// run-server-side (web UI mode).
		results := r.dispatchToolBatch(ctx, sess, executor, pool, toolCalls, out)

		// Check if cancelled during tool execution.
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled during tool execution", Turn: ls.turn}
		}

		// Append tool result messages to session, then inject any NewMessages
		// (e.g., SkillTool injects skill prompts as user messages after tool_result).
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

			// Append NewMessages from tool result (TS newMessages pattern).
			// SkillTool uses this to inject the expanded skill prompt as a
			// user message, so the model treats it as an instruction.
			for _, nm := range results[i].NewMessages {
				sess.AddMessage(nm)
			}
		}

		// ---- Phase 5 (part B): Check stop reason. ----
		if ls.stopReason == "end_turn" && len(toolCalls) > 0 {
			// LLM said end_turn but also requested tools — continue so the LLM
			// can see tool results. This matches the TS behavior.
			continue
		}

		// M4 — emit NextRoundThinking so the channel layer can pre-open
		// a new message card with "正在解读结果" hint. The next callLLM
		// iteration's MessageStart will Set into the same card; the hint
		// gives way to streaming text once first byte lands. Fires only
		// when we're actually doing another LLM round (tools were called).
		if len(toolCalls) > 0 {
			out <- types.EngineEvent{
				Type:    types.EngineEventNextRoundThinking,
				AgentID: "", // main loop
			}
		}

		// Loop continues: the LLM needs to see tool results and respond again.
	}
}

// DispatchToolBatch is the exported entry point reused by the spawn package
// (via the QueryEngine.DispatchToolBatch facade) so sub-agent runs share the
// same per-tool routing policy as the main loop. Internally delegates to
// dispatchToolBatch.
func (r *Runner) DispatchToolBatch(
	ctx context.Context,
	sess *session.Session,
	executor *toolexec.ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	return r.dispatchToolBatch(ctx, sess, executor, pool, toolCalls, out)
}

// dispatchToolBatch routes each tool call to either the client (via
// tool.call) or the server-side executor based on per-tool policy:
//
//   - Tools implementing tool.ClientRoutedTool with IsClientRouted()=true
//     ALWAYS go to the client (e.g. ask_user_question — only the UI can ask
//     a human).
//   - All other tools follow the global QueryEngineConfig.ClientTools
//     flag: true delegates everything (CLI mode), false runs server-side
//     (web UI mode where the server has API keys / sub-agent capability).
//
// The returned slice preserves the original toolCalls order so the LLM
// sees results aligned with its tool_use indices.
func (r *Runner) dispatchToolBatch(
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

	// Partition by routing policy. Indices into the original slice are
	// preserved so we can stitch results back in order.
	var clientCalls, serverCalls []types.ToolCall
	var clientIdx, serverIdx []int
	for i, tc := range toolCalls {
		if r.routeToClient(pool, tc.Name) {
			clientCalls = append(clientCalls, tc)
			clientIdx = append(clientIdx, i)
		} else {
			serverCalls = append(serverCalls, tc)
			serverIdx = append(serverIdx, i)
		}
	}

	// Run server-side batch first — it's typically the long-running side
	// (network calls, sub-agent spawns) and starting it before blocking
	// on the client lets the two run in parallel from goroutines started
	// inside ExecuteBatch.
	if len(serverCalls) > 0 {
		serverResults := executor.ExecuteBatch(ctx, serverCalls, out)
		for j, r := range serverResults {
			results[serverIdx[j]] = r
		}
	}

	if len(clientCalls) > 0 {
		clientResults := r.executeClientTools(ctx, sess, pool, clientCalls, out)
		for j, r := range clientResults {
			results[clientIdx[j]] = r
		}
	}

	return results
}

// routeToClient decides whether a tool call should be sent to the
// connected client (true) or executed server-side (false).
func (r *Runner) routeToClient(pool *tool.ToolPool, toolName string) bool {
	// Per-tool override: tools that can ONLY work client-side opt in via
	// ClientRoutedTool. This bypasses the global flag — even with
	// ClientTools=false (web UI mode), ask_user_question still goes client.
	if t := pool.Get(toolName); t != nil {
		if cr, ok := t.(tool.ClientRoutedTool); ok && cr.IsClientRouted() {
			return true
		}
	}
	// Otherwise honour the global flag.
	return r.deps.LoopConfig().ClientTools
}

// ExecuteClientTools is the exported entry point used by tests that drive
// the client-routing branch in isolation. Production callers go through
// dispatchToolBatch which in turn calls the lowercase variant.
func (r *Runner) ExecuteClientTools(
	ctx context.Context,
	sess *session.Session,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	return r.executeClientTools(ctx, sess, pool, toolCalls, out)
}

// executeClientTools sends tool.call events to the client and waits for
// tool.result responses. Wait semantics depend on the tool kind:
//
//   - Human-interactive tools (ClientRoutedTool, e.g. ask_user_question):
//     wait INDEFINITELY for the user. The only exits are session abort
//     (ctx cancelled) or the client returning a result. Applying an
//     artificial timeout to a human-in-the-loop call would silently
//     drop the user's pending answer — same reasoning as
//     requestPermissionApproval (§6.4.3).
//
//   - Delegation tools (CLI mode, ClientTools=true): apply
//     QueryEngineConfig.ToolTimeout so a crashed or hung client doesn't
//     pin the engine forever. The client can call tool.progress to
//     reset the timer for legitimately long operations.
//
// The pool is needed to look each tool up and check its routing. Calls
// for tools missing from the pool fall through to the delegation
// (timed) branch — defensive default.
func (r *Runner) executeClientTools(
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

		// Emit tool.call event to the client.
		out <- types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		}
	}

	toolTimeout := r.deps.LoopConfig().ToolTimeout
	// Wait for all results. Human-interactive calls don't have a timeout
	// branch in their select — the user gets all the time they need.
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
				// ResolveTool already removed the entry.
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
// the mailbox instead of terminating. This happens when there are async
// sub-agents still running for this session.
func (r *Runner) shouldWaitForAsyncAgents(sessionID string, mailbox *agent.Mailbox) bool {
	if mailbox == nil {
		return false
	}
	reg := r.deps.AgentRegistry()
	if reg == nil {
		return false
	}
	// Check if any async agents spawned by this session are still running.
	return reg.HasRunningForParent(sessionID)
}

// waitForMailboxMessage blocks until a message arrives on the mailbox or the
// context is cancelled. It converts the AgentMessage into a types.Message
// (user role) so the LLM can process the notification in the next turn.
// Returns nil if the context is cancelled before a message arrives.
func (r *Runner) waitForMailboxMessage(
	ctx context.Context,
	sessionID string,
	mailbox *agent.Mailbox,
	out chan<- types.EngineEvent,
) *types.Message {
	if mailbox == nil {
		return nil
	}

	r.deps.Logger().Info("waiting for async agent notifications",
		zap.String("session_id", sessionID),
	)

	select {
	case <-ctx.Done():
		return nil
	case agentMsg, ok := <-mailbox.Receive():
		if !ok || agentMsg == nil {
			return nil
		}
		r.deps.Logger().Info("received async agent notification",
			zap.String("session_id", sessionID),
			zap.String("from", agentMsg.From),
			zap.String("type", string(agentMsg.Type)),
		)

		// Format the notification as a user message for the LLM.
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

// CumulativeUsageFor returns the running token totals for sessionID by
// reading the sessionstats Tracker (the single in-memory truth source).
// Falls back to a zero Usage when stats are not wired in (tests).
func (r *Runner) CumulativeUsageFor(sessionID string) types.Usage {
	if r == nil {
		return types.Usage{}
	}
	statsReg := r.deps.StatsRegistry()
	if statsReg == nil {
		return types.Usage{}
	}
	tr := statsReg.Get(sessionID)
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

// ProcessWithAgent handles a user message routed to a specific agent via @-mention.
// It emits an agent.routed event and delegates to SpawnSync or a team workflow.
func (r *Runner) ProcessWithAgent(
	ctx context.Context,
	sessionID string,
	sess *session.Session,
	mention *MentionResult,
	def *agent.AgentDefinition,
) (<-chan types.EngineEvent, error) {
	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)

		// Emit agent.routed event for client observability.
		out <- types.EngineEvent{
			Type:      types.EngineEventAgentRouted,
			AgentName: def.Name,
			AgentDesc: def.Description,
		}

		prompt := mention.Prompt
		if def.SystemPrompt != "" {
			prompt = def.SystemPrompt + "\n\n" + mention.Prompt
		}

		if def.AutoTeam && len(def.SubAgents) > 0 {
			// Team mode: run as coordinator with predefined sub-agents.
			// TODO: implement runTeamWorkflow for auto-team agents.
			r.deps.Logger().Info("team workflow not yet implemented, falling back to single agent",
				zap.String("agent", def.Name),
			)
		}

		// Single agent mode: SpawnSync directly.
		//
		// SubagentType MUST be def.Name (the registry key), not def.Profile
		// (a prompt-profile selector). SpawnSync uses cfg.SubagentType to
		// look the AgentDefinition back up in the registry — passing the
		// profile name silently misses the lookup, which then makes
		// EffectiveTier() return the default TierCoordinator and bypass the
		// L3 driver routing. scheduler / task tools already pass def.Name;
		// this @-mention path was the odd one out.
		cfg := &agent.SpawnConfig{
			Prompt:          prompt,
			AgentType:       def.AgentType,
			SubagentType:    def.Name,
			Name:            def.Name,
			Description:     def.Description,
			Model:           def.Model,
			MaxTurns:        def.MaxTurns,
			ParentSessionID: sessionID,
			ParentOut:       out, // enable real-time event forwarding
		}

		result, err := r.deps.Spawner().SpawnSync(ctx, cfg)

		if err != nil {
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{
				Type:     types.EngineEventDone,
				Terminal: &types.Terminal{Reason: types.TerminalModelError, Message: err.Error()},
				Usage:    &types.Usage{},
			}
			return
		}

		// Text was already streamed in real-time via ParentOut forwarding.
		// Only emit the final done event.
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: result.Terminal,
			Usage:    result.Usage,
		}
	}()

	return out, nil
}

// --- Free functions (no Runner receiver) ---

// TruncateForDisplay clips a string to n runes for safe inclusion in a
// Display.Summary or .Title field. The "…" suffix signals truncation.
func TruncateForDisplay(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ExtractMessageText extracts the text content from a message's content blocks.
func ExtractMessageText(msg *types.Message) string {
	var buf strings.Builder
	for _, block := range msg.Content {
		if block.Type == types.ContentTypeText {
			buf.WriteString(block.Text)
		}
	}
	return buf.String()
}

// ToolResultFromPayload converts a client-submitted ToolResultPayload to an engine ToolResult.
func ToolResultFromPayload(p *types.ToolResultPayload) types.ToolResult {
	switch p.Status {
	case "success":
		return types.ToolResult{Content: p.Output, IsError: false}
	case "error":
		return types.ToolResult{Content: p.Output + "\n" + p.ErrorMessage, IsError: true}
	case "denied":
		return types.ToolResult{
			Content: fmt.Sprintf("Permission denied: %s", p.ErrorMessage),
			IsError: true,
		}
	case "timeout":
		return types.ToolResult{
			Content: fmt.Sprintf("Execution timed out: %s", p.ErrorMessage),
			IsError: true,
		}
	case "cancelled":
		return types.ToolResult{
			Content: fmt.Sprintf("Execution cancelled: %s", p.ErrorMessage),
			IsError: true,
		}
	default:
		return types.ToolResult{Content: p.Output, IsError: p.Status != "success"}
	}
}

// BuildAssistantMessage creates a Message from the LLM's streamed output.
// reasoning is the thinking-mode chain-of-thought (DeepSeek / o1 / xAI);
// preserved on the Message so the bifrost adapter can replay it on the
// next turn — DeepSeek thinking models reject requests where it's absent.
func BuildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage, reasoning string) types.Message {
	content := make([]types.ContentBlock, 0, 1+len(toolCalls))

	if text != "" {
		content = append(content, types.ContentBlock{
			Type: types.ContentTypeText,
			Text: text,
		})
	}

	for _, tc := range toolCalls {
		content = append(content, types.ContentBlock{
			Type:      types.ContentTypeToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		})
	}

	tokens := 0
	if usage != nil {
		tokens = usage.OutputTokens
	}

	return types.Message{
		Role:             types.RoleAssistant,
		Content:          content,
		CreatedAt:        time.Now(),
		Tokens:           tokens,
		ReasoningContent: reasoning,
	}
}
