package artifacttool

import (
	"context"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// getStore returns the artifact.Store the engine injected into ctx, or
// false if it's missing — which only happens in misconfigured tests.
// We assert here (in the tool package that already imports artifact) so
// the tool-level context_key.go can stay artifact-agnostic.
func getStore(ctx context.Context) (artifact.Store, bool) {
	v, ok := tool.GetArtifactStoreValue(ctx)
	if !ok {
		return nil, false
	}
	s, ok := v.(artifact.Store)
	return s, ok
}

// errResult is a one-liner for tool-error returns. Tools never panic on
// recoverable errors; they hand the LLM a clean message and let it
// re-plan. ErrorType defaults to Internal so callers can still use this
// short form; use errResultTyped when a more specific classification is
// known (which the wire translator surfaces as ErrorInfo.Type).
func errResult(msg string) *types.ToolResult {
	return errResultTyped(msg, types.ToolErrorInternal)
}

// errResultTyped is the same as errResult but lets the caller stamp a
// specific ToolErrorType. Use when the failure shape is well-known
// (invalid input → ToolErrorInvalidInput, missing artifact → also
// invalid_input since the LLM passed a non-existent id, etc.).
func errResultTyped(msg string, errType types.ToolErrorType) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: errType}
}
