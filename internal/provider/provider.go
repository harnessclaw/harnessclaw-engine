// Package provider defines the LLM provider interface.
//
// Implementations wrap specific LLM APIs (Anthropic, OpenAI) behind a
// unified streaming interface. The Bifrost adapter serves as the default
// multi-provider implementation.
package provider

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// ChatRequest contains all parameters for an LLM call.
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []types.Message  `json:"messages"`
	System      string           `json:"system,omitempty"`
	Tools       []ToolSchema     `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature,omitempty"`
	StopReason  string           `json:"stop_reason,omitempty"`
}

// ToolSchema describes a tool for the LLM.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ChatStream wraps a streaming LLM response.
type ChatStream struct {
	// Events delivers streaming events. Closed when the stream ends.
	Events <-chan types.StreamEvent
	// Err returns the terminal error after Events is closed. Nil on success.
	Err func() error
}

// Provider defines the LLM calling interface.
type Provider interface {
	// Chat initiates a streaming conversation with the LLM.
	Chat(ctx context.Context, req *ChatRequest) (*ChatStream, error)

	// CountTokens estimates the token count for the given messages.
	CountTokens(ctx context.Context, messages []types.Message) (int, error)

	// Name returns the provider identifier (e.g. "anthropic", "openai").
	Name() string
}
