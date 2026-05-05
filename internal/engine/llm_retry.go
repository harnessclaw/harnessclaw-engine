package engine

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// llmRetryConfig controls retry behavior for LLM Chat calls.
const (
	llmMaxRetries   = 3
	llmInitialDelay = 1 * time.Second
	llmMaxDelay     = 8 * time.Second
)

// llmCallResult holds the outcome of a single LLM Chat + stream consumption attempt.
type llmCallResult struct {
	textBuf    string
	toolCalls  []types.ToolCall
	stopReason string
	lastUsage  *types.Usage
	streamErr  error // non-nil if the call or stream failed
}

// isRetryableError determines if an LLM error warrants a retry.
// Network errors (i/o timeout, connection reset, EOF) are retryable.
// Auth errors (401/403), prompt_too_long (413) are not.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Network-level errors
	for _, pattern := range []string{
		"i/o timeout",
		"connection reset",
		"connection refused",
		"EOF",
		"broken pipe",
		"no such host",
		"network is unreachable",
		"tls handshake timeout",
		"server error",
		"503",
		"502",
		"500",
		"529",
		"overloaded",
	} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// retryLLMCall attempts to call provider.Chat and consume the stream, retrying
// on transient errors up to llmMaxRetries times with exponential backoff.
//
// When out is non-nil, events are streamed in real-time on the successful
// attempt. Failed attempts buffer silently so partial data is not emitted.
//
// Returns the result of the successful attempt, or the last failed attempt.
func retryLLMCall(
	ctx context.Context,
	prov provider.Provider,
	req *provider.ChatRequest,
	logger *zap.Logger,
	out chan<- types.EngineEvent,
) *llmCallResult {
	var lastResult *llmCallResult
	delay := llmInitialDelay

	for attempt := 0; attempt <= llmMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return &llmCallResult{streamErr: ctx.Err()}
		}

		// First attempt: stream events in real-time (optimistic path).
		// Retries: buffer silently to avoid emitting duplicate/partial data.
		// If the first attempt fails, the error event signals the client.
		var attemptOut chan<- types.EngineEvent
		if attempt == 0 {
			attemptOut = out
		}

		// Timing instrumentation: when something feels "stuck", we want to
		// know which leg is responsible — the round-trip to the LLM
		// gateway, the streaming consumption, or something between turns.
		// One log line per attempt with msg/tool counts on the way in and
		// elapsed + first-byte / completion deltas on the way out gives a
		// clean picture without spamming under healthy traffic.
		startedAt := time.Now()
		logger.Info("llm.call begin",
			zap.Int("attempt", attempt+1),
			zap.Int("messages", len(req.Messages)),
			zap.Int("tools", len(req.Tools)),
			zap.Int("system_chars", len(req.System)),
			zap.Int("max_tokens", req.MaxTokens),
		)
		result := doSingleLLMCall(ctx, prov, req, attemptOut)
		elapsed := time.Since(startedAt)
		if result.streamErr == nil {
			logger.Info("llm.call ok",
				zap.Int("attempt", attempt+1),
				zap.Duration("elapsed", elapsed),
				zap.Int("text_chars", len(result.textBuf)),
				zap.Int("tool_calls", len(result.toolCalls)),
				zap.String("stop_reason", result.stopReason),
			)
			if attempt > 0 {
				logger.Info("LLM call succeeded after retry",
					zap.Int("attempt", attempt+1),
				)
			}
			return result
		}
		logger.Warn("llm.call err",
			zap.Int("attempt", attempt+1),
			zap.Duration("elapsed", elapsed),
			zap.Error(result.streamErr),
		)

		lastResult = result

		// Check if retryable.
		if !isRetryableError(result.streamErr) {
			return result
		}

		if attempt < llmMaxRetries {
			logger.Warn("LLM call failed, retrying",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", llmMaxRetries),
				zap.Duration("backoff", delay),
				zap.Error(result.streamErr),
			)
			select {
			case <-ctx.Done():
				return &llmCallResult{streamErr: ctx.Err()}
			case <-time.After(delay):
			}
			// Exponential backoff.
			delay *= 2
			if delay > llmMaxDelay {
				delay = llmMaxDelay
			}
		}
	}

	return lastResult
}

// doSingleLLMCall performs one Chat call and fully consumes the stream,
// collecting text, tool calls, and usage. When out is non-nil, events are
// also emitted in real-time for streaming to the client.
func doSingleLLMCall(
	ctx context.Context,
	prov provider.Provider,
	req *provider.ChatRequest,
	out chan<- types.EngineEvent,
) *llmCallResult {
	stream, err := prov.Chat(ctx, req)
	if err != nil {
		return &llmCallResult{streamErr: err}
	}

	result := &llmCallResult{}

	for evt := range stream.Events {
		switch evt.Type {
		case types.StreamEventText:
			result.textBuf += evt.Text
			if out != nil {
				out <- types.EngineEvent{Type: types.EngineEventText, Text: evt.Text}
			}
		case types.StreamEventToolUse:
			if evt.ToolCall != nil {
				result.toolCalls = append(result.toolCalls, *evt.ToolCall)
				if out != nil {
					out <- types.EngineEvent{
						Type:      types.EngineEventToolUse,
						ToolUseID: evt.ToolCall.ID,
						ToolName:  evt.ToolCall.Name,
						ToolInput: evt.ToolCall.Input,
					}
				}
			}
		case types.StreamEventMessageEnd:
			result.stopReason = evt.StopReason
			result.lastUsage = evt.Usage
		case types.StreamEventError:
			// In-stream error; will be captured by stream.Err() below.
		}
	}

	if streamErr := stream.Err(); streamErr != nil {
		result.streamErr = streamErr
	}

	return result
}
