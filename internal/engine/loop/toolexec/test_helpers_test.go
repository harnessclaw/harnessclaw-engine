package toolexec

import (
	"context"
	"encoding/json"

	"harnessclaw-go/pkg/types"
)

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
