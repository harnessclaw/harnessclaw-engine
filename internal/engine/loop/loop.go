package loop

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/llmcall"
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
		}
		timeouts := llmcall.LLMTimeouts(0, 0) // use defaults
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
		cfg.Session.AddMessage(assistantMsg)
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
