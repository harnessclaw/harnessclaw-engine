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
// cfg.PermChecker is REQUIRED. The toolexec executor calls
// permChecker.Check on every tool path that isn't pre-allowed; passing
// nil produces a nil-pointer panic deep inside the tool call. The
// caller (tier module / loop.Run precondition) is responsible for
// supplying a non-nil checker — typically BypassChecker for sub-agents
// or an InheritedChecker seeded with the parent session's approved
// tool whitelist (see common.BuildInheritedChecker).
//
// cfg.ApprovalFn is optional; nil falls back to deny-on-Ask (legacy
// sub-agent behavior — they have no UI to surface prompts to).
func dispatchTools(ctx context.Context, cfg *Config, calls []types.ToolCall, logger *zap.Logger) []types.ToolResult {
	exec := toolexec.NewToolExecutor(cfg.Tools, cfg.PermChecker, logger, cfg.ToolTimeout, cfg.ApprovalFn)
	exec.SetAgentScope(cfg.AgentScope)
	return exec.ExecuteBatch(ctx, calls, cfg.Out)
}
