package loop

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/loop/toolexec"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// dispatchTools runs a batch of tool calls. Client-routed tools are
// bridged through the parent WebSocket session; server-side tools keep
// using the toolexec executor.
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
func dispatchTools(ctx context.Context, cfg *Config, turn int, calls []types.ToolCall, logger *zap.Logger) []types.ToolResult {
	results := make([]types.ToolResult, len(calls))

	for start := 0; start < len(calls); {
		clientRouted := routeToClient(cfg.Tools, calls[start].Name)
		end := start + 1
		for end < len(calls) && routeToClient(cfg.Tools, calls[end].Name) == clientRouted {
			end++
		}

		segment := calls[start:end]
		var segmentResults []types.ToolResult
		if clientRouted {
			segmentResults = executeClientTools(ctx, cfg, segment)
		} else {
			segmentResults = executeServerTools(ctx, cfg, segment, logger)
		}

		for i, result := range segmentResults {
			idx := start + i
			results[idx] = result
			if cfg.Hooks.OnToolResult != nil {
				cfg.Hooks.OnToolResult(turn, calls[idx], result)
			}
		}

		start = end
	}

	return results
}

func executeServerTools(ctx context.Context, cfg *Config, calls []types.ToolCall, logger *zap.Logger) []types.ToolResult {
	exec := toolexec.NewToolExecutor(cfg.Tools, cfg.PermChecker, logger, cfg.ToolTimeout, cfg.ApprovalFn)
	exec.SetAgentScope(cfg.AgentScope)
	exec.SetAgentID(cfg.AgentID)
	exec.SetTaskContract(cfg.TaskContract)
	exec.SetArtifactProducer(cfg.ArtifactProducer)
	return exec.ExecuteBatch(ctx, calls, cfg.Out)
}

func routeToClient(pool *tool.ToolPool, toolName string) bool {
	if pool == nil {
		return false
	}
	t := pool.Get(toolName)
	if t == nil {
		return false
	}
	cr, ok := t.(tool.ClientRoutedTool)
	return ok && cr.IsClientRouted()
}

func executeClientTools(ctx context.Context, cfg *Config, calls []types.ToolCall) []types.ToolResult {
	results := make([]types.ToolResult, len(calls))
	awaitSession := cfg.ClientAwaitSession
	if awaitSession == nil {
		awaitSession = cfg.Session
	}
	if awaitSession == nil || awaitSession.Awaits == nil {
		for i, call := range calls {
			results[i] = types.ToolResult{
				Content: fmt.Sprintf("tool %s requires a session await registry", call.Name),
				IsError: true,
			}
		}
		return results
	}
	if cfg.Out == nil {
		for i, call := range calls {
			results[i] = types.ToolResult{
				Content: fmt.Sprintf("tool %s requires a connected client event channel", call.Name),
				IsError: true,
			}
		}
		return results
	}

	awaits := make([]chan *types.ToolResultPayload, len(calls))
	for i, call := range calls {
		await := awaitSession.Awaits.PushTool(call.ID, call.Name)
		awaits[i] = await.Result
		select {
		case cfg.Out <- types.EngineEvent{
			Type:           types.EngineEventToolCall,
			ToolUseID:      call.ID,
			ToolName:       call.Name,
			ToolInput:      call.Input,
			AwaitSessionID: awaitSession.ID,
		}:
		case <-ctx.Done():
			results[i] = types.ToolResult{Content: "execution cancelled", IsError: true}
			awaitSession.Awaits.ForgetTool(call.ID)
		}
	}

	for i, call := range calls {
		if results[i].Content != "" || results[i].IsError {
			continue
		}
		select {
		case <-ctx.Done():
			results[i] = types.ToolResult{Content: "execution cancelled", IsError: true}
			awaitSession.Awaits.ForgetTool(call.ID)
		case payload, ok := <-awaits[i]:
			if !ok {
				results[i] = types.ToolResult{Content: "execution cancelled", IsError: true}
				continue
			}
			results[i] = toolResultFromPayload(payload)
		}
	}

	return results
}

func toolResultFromPayload(p *types.ToolResultPayload) types.ToolResult {
	if p == nil {
		return types.ToolResult{Content: "missing client tool result", IsError: true}
	}
	metadata := p.Metadata
	switch p.Status {
	case "success":
		return types.ToolResult{Content: p.Output, IsError: false, Metadata: metadata}
	case "error":
		return types.ToolResult{Content: p.Output + "\n" + p.ErrorMessage, IsError: true, Metadata: metadata}
	case "denied":
		return types.ToolResult{Content: fmt.Sprintf("Permission denied: %s", p.ErrorMessage), IsError: true, Metadata: metadata}
	case "timeout":
		return types.ToolResult{Content: fmt.Sprintf("Execution timed out: %s", p.ErrorMessage), IsError: true, Metadata: metadata}
	case "cancelled":
		return types.ToolResult{Content: fmt.Sprintf("Execution cancelled: %s", p.ErrorMessage), IsError: true, Metadata: metadata}
	default:
		return types.ToolResult{Content: p.Output, IsError: p.Status != "success", Metadata: metadata}
	}
}
