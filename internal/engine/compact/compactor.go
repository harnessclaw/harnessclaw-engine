// Package compact implements context compression to keep conversations
// within the LLM's token budget.
package compact

import (
	"context"
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
		return false // circuit breaker open
	}

	totalTokens := 0
	for _, m := range messages {
		totalTokens += m.Tokens
	}
	return float64(totalTokens) > float64(maxTokens)*threshold
}

// Compact compresses the message history via LLM summarization.
func (c *LLMCompactor) Compact(ctx context.Context, messages []types.Message) ([]types.Message, error) {
	if len(messages) < 4 {
		// Too few messages to compact meaningfully.
		return messages, nil
	}

	// Micro-compact: if fewer than 10 messages, just truncate early ones.
	if len(messages) < 10 {
		return c.microCompact(messages), nil
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

	c.failureCount.Store(0) // reset on success

	// Replace older messages with a single summary message.
	result := make([]types.Message, 0, len(messages)/3+1)
	result = append(result, types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: summary},
		},
	})
	// Keep recent 1/3 of messages.
	keepFrom := len(messages) * 2 / 3
	result = append(result, messages[keepFrom:]...)
	return result, nil
}

// microCompact keeps only the first message and the most recent half.
func (c *LLMCompactor) microCompact(messages []types.Message) []types.Message {
	if len(messages) <= 2 {
		return messages
	}
	keepFrom := len(messages) / 2
	result := make([]types.Message, 0, len(messages)-keepFrom+1)
	result = append(result, messages[0]) // keep first message for context
	result = append(result, messages[keepFrom:]...)
	return result
}

// summarize calls the LLM to generate a conversation summary.
func (c *LLMCompactor) summarize(ctx context.Context, messages []types.Message) (string, error) {
	summarizeReq := &provider.ChatRequest{
		Messages:  messages,
		System:    "Summarize this conversation concisely, preserving all key decisions, code changes, file paths, and action items. Output only the summary.",
		MaxTokens: 2048,
	}

	stream, err := c.provider.Chat(ctx, summarizeReq)
	if err != nil {
		return "", err
	}

	var summary string
	for evt := range stream.Events {
		if evt.Type == types.StreamEventText {
			summary += evt.Text
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	return summary, nil
}
