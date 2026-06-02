// Package compact implements context compression to keep conversations
// within the LLM's token budget.
package compact

import (
	"context"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// Compactor compresses conversation history when token usage exceeds the budget.
type Compactor interface {
	// ShouldCompact returns true if the messages exceed the token threshold.
	ShouldCompact(messages []types.Message, maxTokens int, threshold float64) bool

	// Compact compresses the message history, returning a shorter sequence.
	Compact(ctx context.Context, messages []types.Message) ([]types.Message, error)
}

// LLMCompactor uses an LLM call to generate a summary of older messages.
type LLMCompactor struct {
	provider     provider.Provider
	logger       *zap.Logger
	failureCount atomic.Int32 // atomic for concurrent safety
	maxFailures  int32        // circuit breaker threshold
}

// NewLLMCompactor creates a compactor that uses the LLM for summarization.
func NewLLMCompactor(p provider.Provider, logger *zap.Logger) *LLMCompactor {
	c := &LLMCompactor{
		provider:    p,
		logger:      logger,
		maxFailures: 3,
	}
	return c
}

// ShouldCompact checks if token usage exceeds threshold.
func (c *LLMCompactor) ShouldCompact(messages []types.Message, maxTokens int, threshold float64) bool {
	if c.failureCount.Load() >= c.maxFailures {
		// Circuit-breaker open: skip compaction. Log so operators
		// notice when ctx is sliding toward the model's input window
		// limit because the compactor stopped helping. Once-per-call
		// at DEBUG keeps volume sane for hot loops.
		if c.logger != nil {
			c.logger.Debug("compact.should: circuit breaker open — skipping compaction",
				zap.Int32("failure_count", c.failureCount.Load()),
				zap.Int32("max_failures", c.maxFailures),
				zap.Int("messages", len(messages)),
			)
		}
		return false
	}

	totalTokens := 0
	for _, m := range messages {
		totalTokens += m.Tokens
	}
	should := float64(totalTokens) > float64(maxTokens)*threshold
	// One line per ShouldCompact tick so we can correlate the spike to
	// the matching Compact() call (or its absence when totalTokens is
	// inflated by a misattributed Message.Tokens field — see loop.go
	// buildAssistantMessage where Tokens = input+output, not the
	// per-message size).
	if c.logger != nil {
		c.logger.Debug("compact.should",
			zap.Int("total_tokens", totalTokens),
			zap.Int("max_tokens", maxTokens),
			zap.Float64("threshold", threshold),
			zap.Bool("should_compact", should),
			zap.Int("messages", len(messages)),
		)
	}
	return should
}

// Compact compresses the message history via LLM summarization.
func (c *LLMCompactor) Compact(ctx context.Context, messages []types.Message) ([]types.Message, error) {
	inN := len(messages)
	if c.logger != nil {
		c.logger.Info("compact.begin",
			zap.Int("in_msgs", inN),
		)
	}
	if len(messages) < 4 {
		// Too few messages to compact meaningfully.
		if c.logger != nil {
			c.logger.Debug("compact.skip: too few messages",
				zap.Int("in_msgs", inN),
			)
		}
		return messages, nil
	}

	// Micro-compact: if fewer than 10 messages, just truncate early ones.
	if len(messages) < 10 {
		out := c.microCompact(messages)
		c.logCompactEnd("micro", inN, out, "", 0)
		return out, nil
	}

	// Full compact: summarize the first 2/3 of messages via LLM call.
	summary, err := c.summarize(ctx, messages)
	if err != nil {
		c.failureCount.Add(1)
		c.logger.Warn("compact failed, circuit breaker count incremented",
			zap.Int32("failure_count", c.failureCount.Load()),
			zap.Error(err),
		)
		return messages, err
	}

	// Empty/whitespace-only summary: the model returned no usable
	// content (the 2026-06-02 freelancer 400 was caused by exactly
	// this — a 56-completion-token response on a channel we don't
	// forward yielded summary=""; we then constructed an
	// assistant{text:""} as the new first message and Anthropic 400'd
	// the next turn).
	// Treat as a failure: bump the circuit breaker so repeated empty
	// summaries open it, and fall back to microCompact so we still
	// reduce ctx size without injecting a malformed assistant block.
	if strings.TrimSpace(summary) == "" {
		c.failureCount.Add(1)
		if c.logger != nil {
			c.logger.Warn("compact.summary_empty: summarize returned no text — falling back to microCompact",
				zap.Int("in_msgs", inN),
				zap.Int("summary_chars", len(summary)),
				zap.Int32("failure_count", c.failureCount.Load()),
			)
		}
		out := c.microCompact(messages)
		c.logCompactEnd("fallback_micro_after_empty_summary", inN, out, "", 0)
		return out, nil
	}

	c.failureCount.Store(0) // reset on success

	// Replace older messages with a single summary message.
	//
	// Wrap as USER (not assistant). Anthropic's API requires the first
	// non-system message to have role=user; making the synthetic
	// summary an assistant message violates that whenever Compact()
	// trims the original leading user turn into the summary half. The
	// "[Prior conversation summary]" prefix makes the framing
	// unambiguous for the model — it reads as injected context, not
	// as a real user turn.
	keepFrom := advancePastOrphanToolResults(messages, len(messages)*2/3)
	result := make([]types.Message, 0, len(messages)-keepFrom+1)
	result = append(result, types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "[Prior conversation summary]\n" + summary},
		},
	})
	result = append(result, messages[keepFrom:]...)
	c.logCompactEnd("full", inN, result, summary, keepFrom)
	return result, nil
}

// logCompactEnd is the single sink for "compact done" observability so
// every exit path of Compact reports a consistent shape: input/output
// counts, the role of the new first message (catches "summary became
// assistant first" regressions), summary length + preview, and the
// trim boundary. mode disambiguates which branch ran: "full",
// "micro", "fallback_micro_after_empty_summary".
func (c *LLMCompactor) logCompactEnd(mode string, inN int, out []types.Message, summary string, keepFrom int) {
	if c.logger == nil {
		return
	}
	firstRole := ""
	if len(out) > 0 {
		firstRole = string(out[0].Role)
	}
	preview := summary
	if len(preview) > 120 {
		preview = preview[:120] + "…"
	}
	c.logger.Info("compact.end",
		zap.String("mode", mode),
		zap.Int("in_msgs", inN),
		zap.Int("out_msgs", len(out)),
		zap.Int("summary_chars", len(summary)),
		zap.String("summary_preview", preview),
		zap.Int("keep_from", keepFrom),
		zap.String("first_role", firstRole),
		zap.Int32("failure_count", c.failureCount.Load()),
	)
}

// microCompact keeps only the first message and the most recent half.
func (c *LLMCompactor) microCompact(messages []types.Message) []types.Message {
	if len(messages) <= 2 {
		return messages
	}
	// Skip orphan tool_result at the boundary — same reason as Compact.
	keepFrom := advancePastOrphanToolResults(messages, len(messages)/2)
	result := make([]types.Message, 0, len(messages)-keepFrom+1)
	result = append(result, messages[0]) // keep first message for context
	result = append(result, messages[keepFrom:]...)
	return result
}

// advancePastOrphanToolResults moves keepFrom forward while the message at
// that index is a tool_result whose matching tool_call assistant lies in the
// discarded prefix. OpenAI's chat-completions schema requires every "tool"
// role message to immediately follow an assistant message that carries the
// matching tool_calls; an orphan tool_result triggers a 400. Anthropic's
// schema is more lenient but the engine targets both, so we sanitize here.
func advancePastOrphanToolResults(messages []types.Message, keepFrom int) int {
	for keepFrom < len(messages) && containsToolResult(messages[keepFrom]) {
		keepFrom++
	}
	return keepFrom
}

// containsToolResult reports whether any content block in m is a
// ContentTypeToolResult — i.e. the message becomes an OpenAI "tool" role
// message during provider conversion.
func containsToolResult(m types.Message) bool {
	for _, b := range m.Content {
		if b.Type == types.ContentTypeToolResult {
			return true
		}
	}
	return false
}

// summarize calls the LLM to generate a conversation summary.
func (c *LLMCompactor) summarize(ctx context.Context, messages []types.Message) (string, error) {
	// The "Reply with the summary text only. Do not call any tools."
	// suffix is defensive: bifrost's adapter dial log on 2026-06-02
	// showed a summarize call returning 56 completion tokens with
	// text_chars=0 / tool_calls=0 / reasoning=0. The smoking-gun
	// theory is the model attempted a tool-use we can't accept (we
	// pass no Tools). Adding this line costs nothing on compliant
	// models and steers stricter ones onto the text channel.
	systemPrompt := "Summarize this conversation concisely, preserving all key decisions, code changes, file paths, and action items. " +
		"Reply with the summary text only. Do not call any tools."

	summarizeReq := &provider.ChatRequest{
		Messages:  messages,
		System:    systemPrompt,
		MaxTokens: 2048,
		// Purpose makes this call distinguishable from the engine's
		// per-turn main loop in bifrost.dial logs — without it the
		// "messages: 10 tools: 0" intermediate call shows up
		// unattributed and looks like a ghost request.
		Purpose: "compact_summary",
	}

	stream, err := c.provider.Chat(ctx, summarizeReq)
	if err != nil {
		return "", err
	}

	var summary string
	var textEvents, toolEvents, errEvents int
	var lastStopReason string
	for evt := range stream.Events {
		switch evt.Type {
		case types.StreamEventText:
			textEvents++
			summary += evt.Text
		case types.StreamEventToolUse:
			toolEvents++
		case types.StreamEventMessageEnd:
			lastStopReason = evt.StopReason
		case types.StreamEventError:
			errEvents++
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	// Per-summarize trace so empty-summary incidents (text_chars=0 but
	// the call returned successfully) can be tied to the upstream
	// stop_reason and the event-shape we actually consumed.
	if c.logger != nil {
		c.logger.Debug("compact.summary",
			zap.Int("chars", len(summary)),
			zap.Int("text_events", textEvents),
			zap.Int("tool_events", toolEvents),
			zap.Int("error_events", errEvents),
			zap.String("stop_reason", lastStopReason),
		)
	}
	return summary, nil
}
