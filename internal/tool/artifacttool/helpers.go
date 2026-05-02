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
// re-plan.
func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true}
}
