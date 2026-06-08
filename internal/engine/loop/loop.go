package loop

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/llmcall"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// Run drives the loop until OnTurnComplete returns Terminate, ctx is
// cancelled, or MaxTurns is reached.
//
// On each turn:
//  1. Auto-compact if Compactor says so.
//  2. Enforce MaxTurns cap.
//  3. Build ChatRequest; call provider via llmcall.CallLLM (with retry).
//  4. Append assistant message to session.
//  5. Dispatch tools (if any); append tool_result messages.
//  6. Call OnTurnComplete; apply Decision (terminate or inject + continue).
func Run(ctx context.Context, cfg *Config) (*Result, error) {
	if cfg.OnTurnComplete == nil {
		return nil, fmt.Errorf("loop.Run: OnTurnComplete required")
	}
	if cfg.MaxTurns <= 0 {
		return nil, fmt.Errorf("loop.Run: MaxTurns must be > 0")
	}
	if cfg.PermChecker == nil {
		return nil, fmt.Errorf("loop.Run: PermChecker required")
	}

	res := &Result{}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	for turn := 1; turn <= cfg.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			res.Terminal = types.Terminal{
				Reason: types.TerminalAbortedStreaming,
				Turn:   turn - 1,
			}
			return res, nil
		}

		// Phase 1: auto-compact (best-effort).
		messages := cfg.Session.GetMessages()
		if cfg.Compactor != nil && cfg.Compactor.ShouldCompact(messages, cfg.ContextWindow, 0.8) {
			compacted, err := cfg.Compactor.Compact(ctx, messages)
			if err == nil {
				cfg.Session.SetMessages(compacted)
				messages = compacted
			}
		}

		// Phase 2: LLM call.
		req := &provider.ChatRequest{
			Messages:      messages,
			System:        cfg.SystemPrompt,
			Tools:         cfg.Tools.Schemas(),
			MaxTokens:     cfg.MaxTokens,
			ContextWindow: cfg.ContextWindow,
			// Purpose tags this as the per-turn assistant call so the
			// bifrost adapter's dial logs can disentangle it from
			// intermediate Chat() calls (compactor summarize, intent
			// extraction, etc.) that share the same provider.
			Purpose: "main_loop",
		}
		// Dump the request shape (same format as emma/runner.go:115)
		// so sub-agent LLM calls are debuggable without re-running.
		// Previously only emma's main loop dumped, leaving sub-agent
		// failures (e.g. "Messages with role 'tool' must be a response
		// to a preceding message with 'tool_calls'") opaque — operators
		// could see the error but not the message sequence that caused
		// it.
		dumpLLMRequest(logger, cfg.AgentID, turn, messages, len(req.Tools), len(cfg.SystemPrompt), cfg.MaxTokens)
		// Pre-flight shape check: scan for the four request-body
		// pathologies that reliably trigger upstream HTTP 400 — empty
		// text blocks, assistant messages with no content blocks,
		// a non-user first message (Anthropic-strict), and orphan
		// tool_result messages whose matching tool_use isn't on the
		// preceding assistant. We only log here (no rewrite) so we get
		// a unique fingerprint when the 400 hits, but tier modules can
		// reuse the same data structure later for a sanitize pass.
		preflightValidateMessages(logger, cfg.AgentID, turn, messages)
		// Honor caller-supplied LLM timeouts. Previously this hard-coded
		// LLMTimeouts(0, 0) with a "use defaults" comment that did
		// nothing — the resulting sub-agent loop had NO API or first-
		// byte watchdog, so a stuck upstream stream (anthropic dial
		// without any chunk) parked the goroutine forever and only
		// got reaped 5–10 min later by the wire card orphan watchdog.
		timeouts := llmcall.LLMTimeouts(cfg.LLMAPITimeout, cfg.LLMFirstByteTimeout)
		llmRes := llmcall.CallLLM(ctx, cfg.Provider, req, logger, cfg.Retryer,
			timeouts, cfg.AgentID, cfg.Out, cfg.Out)
		if llmRes == nil {
			res.Terminal = types.Terminal{
				Reason: types.TerminalModelError, Turn: turn,
			}
			return res, nil
		}
		if llmRes.StreamErr != nil {
			res.Terminal = types.Terminal{
				Reason: types.TerminalModelError, Message: llmRes.StreamErr.Error(), Turn: turn,
			}
			return res, nil
		}

		// Phase 3: assistant message.
		assistantMsg := buildAssistantMessage(llmRes.TextBuf, llmRes.ToolCalls,
			llmRes.LastUsage, llmRes.Reasoning)
		// B6: empty assistant content blocks cannot be sent back to
		// Anthropic (the next turn's request will 400 with "text
		// content blocks must be non-empty" / "all messages must have
		// non-empty content"). Surface a WARN so this leaves a unique
		// fingerprint in the log even when the immediate turn doesn't
		// fail — the 400 always materialises N turns later when the
		// empty msg is replayed. Common triggers: refusal returned
		// without text, mid-stream error wiping TextBuf, upstream
		// "other" channel ate the output.
		if len(assistantMsg.Content) == 0 && logger != nil {
			stopReason := ""
			if llmRes.LastUsage != nil {
				// LastUsage doesn't carry stop_reason today, but
				// llmRes.StopReason does — fall back if available
				// via the type.
				stopReason = llmRes.StopReason
			}
			logger.Warn("loop: assistant message has zero content blocks — replaying it next turn will likely 400",
				zap.String("agent_id", cfg.AgentID),
				zap.Int("turn", turn),
				zap.Int("text_chars", len(llmRes.TextBuf)),
				zap.Int("tool_calls", len(llmRes.ToolCalls)),
				zap.Int("reasoning_chars", len(llmRes.Reasoning)),
				zap.String("stop_reason", stopReason),
			)
		}
		cfg.Session.AddMessage(assistantMsg)
		// B8: trace Message.Tokens as recorded on the session message
		// vs the raw usage we got from the provider. buildAssistantMessage
		// stamps Tokens = InputTokens+OutputTokens (cumulative prompt
		// size, not per-message size), and the compactor uses that
		// field for its ShouldCompact aggregate — divergence here
		// explains "Compactor triggered way earlier than total ctx
		// would suggest". DEBUG so it stays free at INFO production.
		if logger != nil && logger.Core().Enabled(zap.DebugLevel) {
			var in, out int
			if llmRes.LastUsage != nil {
				in = llmRes.LastUsage.InputTokens
				out = llmRes.LastUsage.OutputTokens
			}
			logger.Debug("session: assistant message added",
				zap.String("agent_id", cfg.AgentID),
				zap.Int("turn", turn),
				zap.Int("content_blocks", len(assistantMsg.Content)),
				zap.Int("msg_tokens_field", assistantMsg.Tokens),
				zap.Int("usage_input_tokens", in),
				zap.Int("usage_output_tokens", out),
			)
		}
		res.LastMessage = &assistantMsg
		res.NumTurns = turn
		if llmRes.LastUsage != nil {
			res.Usage.InputTokens += llmRes.LastUsage.InputTokens
			res.Usage.OutputTokens += llmRes.LastUsage.OutputTokens
			res.Usage.CacheRead += llmRes.LastUsage.CacheRead
			res.Usage.CacheWrite += llmRes.LastUsage.CacheWrite
		}

		// Phase 4: tool dispatch.
		var toolResults []types.ToolResult
		if len(llmRes.ToolCalls) > 0 {
			toolResults = dispatchTools(ctx, cfg, llmRes.ToolCalls, logger)
			res.LastToolResults = toolResults
			appendToolResultsToSession(cfg.Session, llmRes.ToolCalls, toolResults)
		}

		// Phase 5: hook decides.
		decision := cfg.OnTurnComplete(turn, assistantMsg, toolResults)
		if decision.Terminate != nil {
			res.Terminal = *decision.Terminate
			return res, nil
		}
		for _, m := range decision.Inject {
			cfg.Session.AddMessage(m)
		}
	}

	res.Terminal = types.Terminal{
		Reason:  types.TerminalMaxTurns,
		Message: fmt.Sprintf("max turns reached (%d)", cfg.MaxTurns),
		Turn:    cfg.MaxTurns,
	}
	return res, nil
}

// preflightValidateMessages scans the outgoing message slice for the
// four request-body pathologies that produce upstream HTTP 400 on
// Anthropic / OpenAI-strict providers:
//
//  1. Empty text content blocks  (text=="" inside ContentTypeText)
//  2. Messages with no Content blocks at all (typically assistant)
//  3. First non-system message not having role=user (Anthropic only)
//  4. tool_result messages whose ToolUseID has no matching tool_use on
//     the immediately preceding assistant message (OpenAI-strict;
//     bifrost's convertMessages does drop these later but emitting a
//     WARN here lets us spot the source while the trail is hot).
//
// Behaviour is observe-only: we never mutate `messages`. A 4xx hit after
// this log gives operators the exact index/role/block that broke the
// schema without re-running the failing job.
//
// Only logs when at least one issue is found, so the steady state is
// silent.
func preflightValidateMessages(logger *zap.Logger, agentID string, turn int, messages []types.Message) {
	if logger == nil || len(messages) == 0 {
		return
	}
	type issue struct {
		Index   int    `json:"index"`
		Role    string `json:"role"`
		Block   int    `json:"block,omitempty"`
		Kind    string `json:"kind"`
		Detail  string `json:"detail,omitempty"`
	}
	var issues []issue

	// Rule 3: first non-system must be user.
	first := messages[0]
	if first.Role != types.RoleUser {
		issues = append(issues, issue{
			Index: 0, Role: string(first.Role),
			Kind:   "first_message_not_user",
			Detail: "Anthropic requires the first non-system message to have role=user",
		})
	}

	// Build a map of tool_use IDs visible on each assistant message so
	// we can detect orphan tool_result entries (rule 4). Map value is
	// the assistant message index that produced the ToolUse.
	prevAssistantToolUseIDs := map[string]int{}
	for i, m := range messages {
		// Track tool_use ids on assistant for orphan detection.
		if m.Role == types.RoleAssistant {
			// Reset the map: tool_results must match the IMMEDIATELY
			// preceding assistant — older assistant tool_use entries
			// don't count for OpenAI-strict pairing.
			prevAssistantToolUseIDs = map[string]int{}
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolUse && b.ToolUseID != "" {
					prevAssistantToolUseIDs[b.ToolUseID] = i
				}
			}
		}

		// Rule 2: no content blocks.
		if len(m.Content) == 0 {
			issues = append(issues, issue{
				Index: i, Role: string(m.Role),
				Kind:   "message_no_content",
				Detail: "message has zero content blocks",
			})
			continue
		}

		for bi, b := range m.Content {
			switch b.Type {
			case types.ContentTypeText:
				// Rule 1: empty text block.
				if len(b.Text) == 0 {
					issues = append(issues, issue{
						Index: i, Role: string(m.Role), Block: bi,
						Kind: "empty_text_block",
					})
				}
			case types.ContentTypeToolResult:
				// Rule 4: orphan tool_result on user-role message.
				if m.Role == types.RoleUser {
					if _, ok := prevAssistantToolUseIDs[b.ToolUseID]; !ok {
						issues = append(issues, issue{
							Index: i, Role: string(m.Role), Block: bi,
							Kind:   "orphan_tool_result",
							Detail: "tool_use_id=" + b.ToolUseID + " has no matching tool_use on the preceding assistant message",
						})
					}
				}
			}
		}
	}

	if len(issues) == 0 {
		return
	}
	// Roles in order — useful for "spot the wrong sequence" eyeballing.
	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, string(m.Role))
	}
	logger.Warn("loop: pre-flight message-shape issues — upstream may reject this request",
		zap.String("agent_id", agentID),
		zap.Int("turn", turn),
		zap.Int("message_count", len(messages)),
		zap.Strings("roles", roles),
		zap.Any("issues", issues),
	)
}

// dumpLLMRequest writes a one-block-per-turn debug summary of the
// session messages headed to the LLM. Same shape as emma/runner.go's
// dump so existing log-mining scripts work across L1 and sub-agent
// turns. Cheap when zap level is above debug (Sugar/Field cost only).
func dumpLLMRequest(logger *zap.Logger, agentID string, turn int, messages []types.Message, toolSchemas, sysPromptLen, maxTokens int) {
	if logger == nil {
		return
	}
	logger.Debug("========== LLM REQUEST DUMP START ==========",
		zap.String("agent_id", agentID),
		zap.Int("turn", turn),
		zap.Int("message_count", len(messages)),
		zap.Int("tool_schema_count", toolSchemas),
		zap.Int("system_prompt_len", sysPromptLen),
		zap.Int("max_tokens", maxTokens),
	)
	for i, m := range messages {
		preview := ""
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeText && len(cb.Text) > 0 {
				p := cb.Text
				if len(p) > 200 {
					p = p[:200] + "...[truncated]"
				}
				preview = p
				break
			}
			if cb.Type == types.ContentTypeToolUse {
				preview = fmt.Sprintf("[tool_use: %s]", cb.ToolName)
				break
			}
			if cb.Type == types.ContentTypeToolResult {
				p := cb.ToolResult
				if len(p) > 100 {
					p = p[:100] + "...[truncated]"
				}
				preview = fmt.Sprintf("[tool_result: %s] %s", cb.ToolName, p)
				break
			}
		}
		logger.Debug("llm request message",
			zap.Int("index", i),
			zap.String("role", string(m.Role)),
			zap.Int("content_blocks", len(m.Content)),
			zap.Int("tokens", m.Tokens),
			zap.String("preview", preview),
		)
	}
	logger.Debug("========== LLM REQUEST DUMP END ==========")
}

func buildAssistantMessage(text string, toolCalls []types.ToolCall,
	usage *types.Usage, reasoning string) types.Message {
	content := []types.ContentBlock{}
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
	msg := types.Message{Role: types.RoleAssistant, Content: content}
	if usage != nil {
		msg.Tokens = usage.InputTokens + usage.OutputTokens
	}
	if reasoning != "" {
		msg.ReasoningContent = reasoning
	}
	return msg
}

func appendToolResultsToSession(sess sessionLike, calls []types.ToolCall, results []types.ToolResult) {
	for i, tc := range calls {
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type:       types.ContentTypeToolResult,
				ToolUseID:  tc.ID,
				ToolName:   tc.Name,
				ToolResult: results[i].Content,
				IsError:    results[i].IsError,
			}},
		})
		for _, nm := range results[i].NewMessages {
			sess.AddMessage(nm)
		}
	}
}

// sessionLike narrows the session API the loop needs (lets tests use a
// fake without depending on the full session.Session type).
type sessionLike interface {
	GetMessages() []types.Message
	SetMessages([]types.Message)
	AddMessage(types.Message)
}
