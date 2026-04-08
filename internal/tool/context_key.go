package tool

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// contextKey is the unexported key type for ToolUseContext in context.Context.
type contextKey struct{}

// toolUseContextKey is the singleton key used to store/retrieve ToolUseContext.
var toolUseContextKey = contextKey{}

// GetToolUseContext extracts the ToolUseContext from a context.Context.
// Returns the context and true if found, nil and false otherwise.
func GetToolUseContext(ctx context.Context) (*types.ToolUseContext, bool) {
	tuc, ok := ctx.Value(toolUseContextKey).(*types.ToolUseContext)
	return tuc, ok
}

// WithToolUseContext returns a child context carrying the ToolUseContext.
func WithToolUseContext(ctx context.Context, tuc *types.ToolUseContext) context.Context {
	return context.WithValue(ctx, toolUseContextKey, tuc)
}
