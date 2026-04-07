package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// QueryEngineConfig holds tunables for the query loop.
type QueryEngineConfig struct {
	MaxTurns             int
	AutoCompactThreshold float64
	ToolTimeout          time.Duration
	MaxTokens            int
	SystemPrompt         string
	// ClientTools enables client-side tool execution mode.
	// When true, tool calls are sent to the client via tool_call events
	// instead of being executed server-side.
	ClientTools bool
}

// DefaultQueryEngineConfig returns production defaults.
func DefaultQueryEngineConfig() QueryEngineConfig {
	return QueryEngineConfig{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          120 * time.Second,
		MaxTokens:            16384,
		SystemPrompt:         "You are a helpful assistant.",
		ClientTools:          true,
	}
}

// pendingToolCall tracks a tool call awaiting client result.
type pendingToolCall struct {
	resultCh chan *types.ToolResultPayload
}

// QueryEngine is the concrete Engine implementation that runs the 5-phase query loop.
type QueryEngine struct {
	provider    provider.Provider
	registry    *tool.Registry
	sessionMgr  *session.Manager
	compactor   compact.Compactor
	permChecker permission.Checker
	eventBus    *event.Bus
	logger      *zap.Logger
	config      QueryEngineConfig

	// In-flight session tracking for abort support.
	mu      sync.Mutex
	cancels map[string]context.CancelFunc

	// Pending tool calls awaiting client results (client-tools mode).
	toolMu       sync.Mutex
	pendingTools map[string]*pendingToolCall // tool_use_id → pending
}

// NewQueryEngine creates a new query engine.
func NewQueryEngine(
	prov provider.Provider,
	reg *tool.Registry,
	mgr *session.Manager,
	comp compact.Compactor,
	perm permission.Checker,
	bus *event.Bus,
	logger *zap.Logger,
	cfg QueryEngineConfig,
) *QueryEngine {
	return &QueryEngine{
		provider:     prov,
		registry:     reg,
		sessionMgr:   mgr,
		compactor:    comp,
		permChecker:  perm,
		eventBus:     bus,
		logger:       logger,
		config:       cfg,
		cancels:      make(map[string]context.CancelFunc),
		pendingTools: make(map[string]*pendingToolCall),
	}
}

// ProcessMessage implements Engine. It appends the user message to the session
// and runs the query loop, emitting events on the returned channel.
func (qe *QueryEngine) ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error) {
	// Retrieve or create the session.
	sess, err := qe.sessionMgr.GetOrCreate(ctx, sessionID, "", "")
	if err != nil {
		return nil, fmt.Errorf("session get-or-create: %w", err)
	}

	sess.AddMessage(*msg)

	// Create a cancellable context for this query.
	qCtx, cancel := context.WithCancel(ctx)

	qe.mu.Lock()
	qe.cancels[sessionID] = cancel
	qe.mu.Unlock()

	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)
		defer func() {
			qe.mu.Lock()
			delete(qe.cancels, sessionID)
			qe.mu.Unlock()
			cancel()
		}()

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryStarted,
			Payload: map[string]string{"session_id": sessionID},
		})

		terminal := qe.runQueryLoop(qCtx, sess, out)

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryCompleted,
			Payload: map[string]any{"session_id": sessionID, "reason": terminal.Reason},
		})

		cumUsage := qe.cumulativeUsageFor(sess.ID)
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: &terminal,
			Usage:    &cumUsage,
		}
	}()

	return out, nil
}

// SubmitToolResult implements Engine. It delivers a client-side tool result
// to the waiting query loop goroutine.
func (qe *QueryEngine) SubmitToolResult(_ context.Context, _ string, result *types.ToolResultPayload) error {
	qe.toolMu.Lock()
	pending, ok := qe.pendingTools[result.ToolUseID]
	qe.toolMu.Unlock()

	if !ok {
		return fmt.Errorf("no pending tool call for tool_use_id %s", result.ToolUseID)
	}

	select {
	case pending.resultCh <- result:
		return nil
	default:
		return fmt.Errorf("tool result channel full for %s", result.ToolUseID)
	}
}

// AbortSession implements Engine. Cancels the in-flight query for a session.
func (qe *QueryEngine) AbortSession(_ context.Context, sessionID string) error {
	qe.mu.Lock()
	cancel, ok := qe.cancels[sessionID]
	qe.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active query for session %s", sessionID)
	}
	cancel()
	return nil
}

// --- loopState tracks mutable state across turns of the query loop. ---

type loopState struct {
	turn            int
	stopReason      string
	lastUsage       *types.Usage
	cumulativeUsage types.Usage
}

// runQueryLoop implements the 5-phase query loop modeled on TypeScript query.ts.
//
// Phase 1: Preprocess — assemble system prompt, tool schemas, compact if needed.
// Phase 2: LLM Call — stream the response, collect text + tool calls.
// Phase 3: Error Recovery — handle prompt_too_long, max_output_tokens, rate limits.
// Phase 4: Tool Execution — run requested tools (server-side or client-side).
// Phase 5: Continuation — check terminal conditions, append assistant + tool messages, loop.
func (qe *QueryEngine) runQueryLoop(ctx context.Context, sess *session.Session, out chan<- types.EngineEvent) types.Terminal {
	ls := &loopState{}
	executor := NewToolExecutor(qe.registry, qe.permChecker, qe.logger, qe.config.ToolTimeout)

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		// Auto-compact if needed.
		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, qe.config.MaxTokens, qe.config.AutoCompactThreshold) {
			qe.logger.Info("auto-compact triggered", zap.String("session_id", sess.ID), zap.Int("msg_count", len(messages)))
			qe.eventBus.Publish(event.Event{Topic: event.TopicCompactTriggered, Payload: map[string]string{"session_id": sess.ID}})

			compacted, err := qe.compactor.Compact(ctx, messages)
			if err != nil {
				qe.logger.Warn("auto-compact failed, continuing with full history", zap.Error(err))
			} else {
				// Replace session messages with compacted history.
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		// Check max turns.
		if ls.turn > qe.config.MaxTurns {
			return types.Terminal{
				Reason:  types.TerminalMaxTurns,
				Message: fmt.Sprintf("reached max turns (%d)", qe.config.MaxTurns),
				Turn:    ls.turn - 1,
			}
		}

		// Build the LLM request.
		req := &provider.ChatRequest{
			Messages:  messages,
			System:    qe.config.SystemPrompt,
			Tools:     qe.registry.Schemas(),
			MaxTokens: qe.config.MaxTokens,
		}

		// ---- Phase 2: LLM Call (streaming) ----

		// Emit message.start before streaming begins.
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     qe.provider.Name(),
			Usage: &types.Usage{
				InputTokens: ls.cumulativeUsage.InputTokens,
			},
		}

		stream, err := qe.provider.Chat(ctx, req)
		if err != nil {
			// ---- Phase 3: Error Recovery (connection/auth errors) ----

			// Emit message.delta + message.stop even on error so the message lifecycle is complete.
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error"}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "query cancelled", Turn: ls.turn}
			}
			return types.Terminal{Reason: types.TerminalModelError, Message: err.Error(), Turn: ls.turn}
		}

		// Collect the streamed response.
		var textBuf string
		var toolCalls []types.ToolCall

		for evt := range stream.Events {
			switch evt.Type {
			case types.StreamEventText:
				textBuf += evt.Text
				out <- types.EngineEvent{Type: types.EngineEventText, Text: evt.Text}

			case types.StreamEventToolUse:
				if evt.ToolCall != nil {
					toolCalls = append(toolCalls, *evt.ToolCall)
					// Emit tool_use content block so clients can see what the LLM requested.
					out <- types.EngineEvent{
						Type:      types.EngineEventToolUse,
						ToolUseID: evt.ToolCall.ID,
						ToolName:  evt.ToolCall.Name,
						ToolInput: evt.ToolCall.Input,
					}
				}

			case types.StreamEventMessageEnd:
				ls.stopReason = evt.StopReason
				if evt.Usage != nil {
					ls.lastUsage = evt.Usage
					ls.cumulativeUsage.InputTokens += evt.Usage.InputTokens
					ls.cumulativeUsage.OutputTokens += evt.Usage.OutputTokens
					ls.cumulativeUsage.CacheRead += evt.Usage.CacheRead
					ls.cumulativeUsage.CacheWrite += evt.Usage.CacheWrite
				}

			case types.StreamEventError:
				if evt.Error != nil {
					out <- types.EngineEvent{Type: types.EngineEventError, Error: evt.Error}
				}
			}
		}

		if streamErr := stream.Err(); streamErr != nil {
			// Emit message lifecycle events to close cleanly.
			out <- types.EngineEvent{
				Type:       types.EngineEventMessageDelta,
				StopReason: "error",
				Usage:      ls.lastUsage,
			}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "streaming aborted", Turn: ls.turn}
			}
			// Phase 3: treat stream errors as model errors.
			return types.Terminal{Reason: types.TerminalModelError, Message: streamErr.Error(), Turn: ls.turn}
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

		// Append assistant message to session.
		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): Check terminal — no tool calls means LLM is done. ----
		if len(toolCalls) == 0 {
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

		var results []types.ToolResult

		if qe.config.ClientTools {
			// Client-side tool execution: emit tool.call events and wait for results.
			results = qe.executeClientTools(ctx, toolCalls, out)
		} else {
			// Server-side tool execution (legacy).
			results = executor.ExecuteBatch(ctx, toolCalls, out)
		}

		// Check if cancelled during tool execution.
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled during tool execution", Turn: ls.turn}
		}

		// Append tool result messages to session.
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
		}

		// ---- Phase 5 (part B): Check stop reason. ----
		if ls.stopReason == "end_turn" && len(toolCalls) > 0 {
			// LLM said end_turn but also requested tools — continue so the LLM
			// can see tool results. This matches the TS behavior.
			continue
		}

		// Loop continues: the LLM needs to see tool results and respond again.
	}
}

// executeClientTools sends tool.call events to the client and waits for tool.result responses.
func (qe *QueryEngine) executeClientTools(
	ctx context.Context,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	results := make([]types.ToolResult, len(toolCalls))

	// Register pending tool calls.
	pendingChs := make([]chan *types.ToolResultPayload, len(toolCalls))
	for i, tc := range toolCalls {
		ch := make(chan *types.ToolResultPayload, 1)
		pendingChs[i] = ch
		qe.toolMu.Lock()
		qe.pendingTools[tc.ID] = &pendingToolCall{resultCh: ch}
		qe.toolMu.Unlock()

		// Emit tool.call event to the client.
		out <- types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		}
	}

	// Wait for all results.
	for i, tc := range toolCalls {
		select {
		case <-ctx.Done():
			results[i] = types.ToolResult{
				Content: "execution cancelled",
				IsError: true,
			}
		case payload := <-pendingChs[i]:
			results[i] = toolResultFromPayload(payload)
		case <-time.After(qe.config.ToolTimeout):
			results[i] = types.ToolResult{
				Content: fmt.Sprintf("tool %s timed out waiting for client result", tc.Name),
				IsError: true,
			}
		}

		// Clean up pending entry.
		qe.toolMu.Lock()
		delete(qe.pendingTools, tc.ID)
		qe.toolMu.Unlock()
	}

	return results
}

// toolResultFromPayload converts a client-submitted ToolResultPayload to an engine ToolResult.
func toolResultFromPayload(p *types.ToolResultPayload) types.ToolResult {
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

// cumulativeUsageFor returns a placeholder cumulative usage.
// The real implementation tracks per-session usage.
func (qe *QueryEngine) cumulativeUsageFor(_ string) types.Usage {
	return types.Usage{}
}

// buildAssistantMessage creates a Message from the LLM's streamed output.
func buildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage) types.Message {
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
		Role:      types.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now(),
		Tokens:    tokens,
	}
}
