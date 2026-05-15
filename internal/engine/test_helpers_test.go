package engine

import (
	"context"
	"encoding/json"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// engineFakeProv implements provider.Provider for engine package tests.
// Named engineFakeProv (not fakeProv) to avoid collisions with other packages
// that also define a local fakeProv in the same package under test.
type engineFakeProv struct {
	events []types.StreamEvent
	err    error
}

func (f *engineFakeProv) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan types.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func (f *engineFakeProv) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func (f *engineFakeProv) Name() string { return "engineFakeProv" }

// captureToolImpl is a minimal tool.Tool that records the ctx it receives.
// Tests embed this to assert on ctx values injected by the executor.
type captureToolImpl struct {
	gotCtx context.Context
	input  json.RawMessage
}

func (c *captureToolImpl) Name() string        { return "Capture" }
func (c *captureToolImpl) Description() string { return "captures ctx for tests" }
func (c *captureToolImpl) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "number"},
		},
	}
}
func (c *captureToolImpl) IsReadOnly() bool                          { return true }
func (c *captureToolImpl) IsEnabled() bool                           { return true }
func (c *captureToolImpl) IsConcurrencySafe() bool                   { return true }
func (c *captureToolImpl) ValidateInput(_ json.RawMessage) error      { return nil }
func (c *captureToolImpl) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	c.gotCtx = ctx
	c.input = input
	return &types.ToolResult{Content: "ok"}, nil
}
