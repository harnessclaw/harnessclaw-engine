// Package compact implements context compression to keep conversations
// within the LLM's token budget.
package compact

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.uber.org/zap"
	"harnessclaw-go/internal/artifact"
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
	provider      provider.Provider
	logger        *zap.Logger
	failureCount  atomic.Int32 // atomic for concurrent safety
	maxFailures   int32        // circuit breaker threshold
	artifactStore *artifact.Store
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

// SetArtifactStore sets the artifact store used for artifact-aware compaction.
// When set, large tool results with artifact IDs are replaced with compact
// references before LLM summarization, making compaction lossless for
// tool outputs.
func (c *LLMCompactor) SetArtifactStore(store *artifact.Store) {
	c.artifactStore = store
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

	// Artifact-aware pre-processing: replace large tool results that have
	// artifact IDs with compact references before sending to the LLM.
	// This reduces the summarization input significantly and makes
	// compaction lossless for tool outputs.
	preprocessed := c.replaceArtifactContent(messages)

	// Full compact: summarize the first 2/3 of messages via LLM call.
	summary, err := c.summarize(ctx, preprocessed)
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

// replaceArtifactContent returns a copy of messages where tool_result blocks
// with artifact IDs have their content replaced with compact reference strings.
// If no artifact store is set, messages are returned unchanged.
func (c *LLMCompactor) replaceArtifactContent(messages []types.Message) []types.Message {
	if c.artifactStore == nil {
		return messages
	}

	result := make([]types.Message, len(messages))
	for i, msg := range messages {
		result[i] = c.replaceArtifactInMessage(msg)
	}
	return result
}

// replaceArtifactInMessage returns a copy of msg with tool_result blocks
// containing artifact IDs replaced by compact references from the store.
func (c *LLMCompactor) replaceArtifactInMessage(msg types.Message) types.Message {
	if len(msg.Content) == 0 {
		return msg
	}

	needsCopy := false
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolResult && cb.ArtifactID != "" {
			needsCopy = true
			break
		}
	}
	if !needsCopy {
		return msg
	}

	cp := msg
	cp.Content = make([]types.ContentBlock, len(msg.Content))
	for j, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolResult && cb.ArtifactID != "" {
			ref := c.artifactStore.Ref(cb.ArtifactID)
			if ref != "" {
				cb.ToolResult = ref
			}
		}
		cp.Content[j] = cb
	}
	return cp
}

// summarize calls the LLM to generate a conversation summary.
func (c *LLMCompactor) summarize(ctx context.Context, messages []types.Message) (string, error) {
	// Include artifact context in the summarization prompt when artifacts exist.
	systemPrompt := "Summarize this conversation concisely, preserving all key decisions, code changes, file paths, and action items. Output only the summary."
	if c.artifactStore != nil && c.artifactStore.Len() > 0 {
		systemPrompt += fmt.Sprintf(
			"\n\nNote: Some tool results have been replaced with artifact references (art_XXXX). "+
				"These are stored separately and can be retrieved later. "+
				"Preserve artifact IDs in your summary so they remain accessible. "+
				"There are %d artifacts totaling %d bytes.",
			c.artifactStore.Len(), c.artifactStore.TotalSize(),
		)
	}

	summarizeReq := &provider.ChatRequest{
		Messages:  messages,
		System:    systemPrompt,
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
