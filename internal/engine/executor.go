package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolExecutor handles the execution of tool calls within the query loop.
// It enforces permission checks, timeouts, and the parallel-read / serial-write
// execution model that mirrors the TypeScript engine.
type ToolExecutor struct {
	registry    *tool.Registry
	permChecker permission.Checker
	logger      *zap.Logger
	timeout     time.Duration
}

// NewToolExecutor creates a tool executor.
func NewToolExecutor(
	reg *tool.Registry,
	perm permission.Checker,
	logger *zap.Logger,
	timeout time.Duration,
) *ToolExecutor {
	return &ToolExecutor{
		registry:    reg,
		permChecker: perm,
		logger:      logger,
		timeout:     timeout,
	}
}

// ExecuteBatch runs a batch of tool calls. Read-only and concurrency-safe tools
// execute in parallel; all others execute serially. Results are returned in the
// same order as the input toolCalls slice.
//
// Engine events (ToolStart, ToolEnd) are emitted to `out` for real-time streaming.
func (te *ToolExecutor) ExecuteBatch(
	ctx context.Context,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	results := make([]types.ToolResult, len(toolCalls))

	// Partition into parallel-safe and serial groups.
	type indexedCall struct {
		index int
		call  types.ToolCall
	}
	var parallel, serial []indexedCall

	for i, tc := range toolCalls {
		t := te.registry.Get(tc.Name)
		if t != nil && (t.IsReadOnly() || t.IsConcurrencySafe()) {
			parallel = append(parallel, indexedCall{index: i, call: tc})
		} else {
			serial = append(serial, indexedCall{index: i, call: tc})
		}
	}

	// Execute parallel-safe tools concurrently.
	if len(parallel) > 0 {
		var wg sync.WaitGroup
		wg.Add(len(parallel))
		for _, ic := range parallel {
			go func(ic indexedCall) {
				defer wg.Done()
				results[ic.index] = te.executeSingle(ctx, ic.call, out)
			}(ic)
		}
		wg.Wait()
	}

	// Execute serial tools one at a time.
	for _, ic := range serial {
		if ctx.Err() != nil {
			results[ic.index] = types.ToolResult{
				Content: "execution cancelled",
				IsError: true,
			}
			continue
		}
		results[ic.index] = te.executeSingle(ctx, ic.call, out)
	}

	return results
}

// executeSingle runs one tool call with permission check, timeout, and panic recovery.
func (te *ToolExecutor) executeSingle(
	ctx context.Context,
	tc types.ToolCall,
	out chan<- types.EngineEvent,
) (result types.ToolResult) {
	// Emit tool_start event.
	out <- types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolUseID: tc.ID,
		ToolName:  tc.Name,
		ToolInput: tc.Input,
	}

	defer func() {
		// Emit tool_end event.
		out <- types.EngineEvent{
			Type:       types.EngineEventToolEnd,
			ToolUseID:  tc.ID,
			ToolName:   tc.Name,
			ToolResult: &result,
		}
	}()

	// Panic recovery — a tool must never crash the engine.
	defer func() {
		if r := recover(); r != nil {
			te.logger.Error("tool panicked",
				zap.String("tool", tc.Name),
				zap.Any("panic", r),
			)
			result = types.ToolResult{
				Content: fmt.Sprintf("internal error: tool %s panicked", tc.Name),
				IsError: true,
			}
		}
	}()

	// Look up tool.
	t := te.registry.Get(tc.Name)
	if t == nil {
		return types.ToolResult{
			Content: fmt.Sprintf("unknown tool: %s", tc.Name),
			IsError: true,
		}
	}

	// Check enabled.
	if !t.IsEnabled() {
		return types.ToolResult{
			Content: fmt.Sprintf("tool %s is disabled", tc.Name),
			IsError: true,
		}
	}

	// Validate input.
	rawInput := json.RawMessage(tc.Input)
	if err := t.ValidateInput(rawInput); err != nil {
		return types.ToolResult{
			Content: fmt.Sprintf("invalid input for %s: %v", tc.Name, err),
			IsError: true,
		}
	}

	// Permission check.
	permResult := te.permChecker.Check(ctx, tc.Name, rawInput, t.IsReadOnly())
	switch permResult.Decision {
	case permission.Deny:
		return types.ToolResult{
			Content: fmt.Sprintf("permission denied for %s: %s", tc.Name, permResult.Message),
			IsError: true,
		}
	case permission.Ask:
		// In the multi-channel service model, Ask means "needs approval".
		// For now, treat as denied with a descriptive message.
		// A real implementation would send an approval request to the channel.
		return types.ToolResult{
			Content: fmt.Sprintf("tool %s requires approval: %s", tc.Name, permResult.Message),
			IsError: true,
		}
	}

	// Execute with timeout.
	execCtx, cancel := context.WithTimeout(ctx, te.timeout)
	defer cancel()

	tr, err := t.Execute(execCtx, rawInput)
	if err != nil {
		te.logger.Warn("tool execution failed",
			zap.String("tool", tc.Name),
			zap.Error(err),
		)
		return types.ToolResult{
			Content: fmt.Sprintf("tool %s failed: %v", tc.Name, err),
			IsError: true,
		}
	}

	if tr == nil {
		return types.ToolResult{Content: ""}
	}
	return *tr
}
