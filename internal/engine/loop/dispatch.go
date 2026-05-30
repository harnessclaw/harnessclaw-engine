package loop

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/toolexec"
	"harnessclaw-go/pkg/types"
)

// dispatchTools runs a batch of tool calls server-side using the
// engine's toolexec executor. Client-routed tools (askUserQuestion etc.)
// are NOT handled here — tier modules that need client routing must
// wrap their own loop call or extend Config with a routing decision.
//
// For now, all tools go through server-side execution; client routing
// is a future extension.
func dispatchTools(ctx context.Context, cfg *Config, calls []types.ToolCall, logger *zap.Logger) []types.ToolResult {
	// Permission checker and approval handler are caller's concern;
	// the loop module is intentionally tier-agnostic so we pass nil
	// here. Tier modules that need permission gating wrap their own
	// executor today. Stage-6 work will hoist these into Config when
	// the migration calls for it.
	exec := toolexec.NewToolExecutor(cfg.Tools, nil, logger, cfg.ToolTimeout, nil)
	return exec.ExecuteBatch(ctx, calls, cfg.Out)
}
