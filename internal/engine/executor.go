package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// PermissionApprovalFunc is called when a tool needs user approval (permission.Ask).
// It sends the request to the client and blocks until a response is received.
type PermissionApprovalFunc func(ctx context.Context, out chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse

// ToolExecutor handles the execution of tool calls within the query loop.
// It enforces permission checks, timeouts, and the parallel-read / serial-write
// execution model that mirrors the TypeScript engine.
type ToolExecutor struct {
	pool          *tool.ToolPool
	permChecker   permission.Checker
	logger        *zap.Logger
	timeout       time.Duration
	approvalFn    PermissionApprovalFunc // nil = deny on Ask (legacy behavior)
	artifactStore tool.ArtifactStore     // optional; injected into tool context when non-nil
}

// NewToolExecutor creates a tool executor.
func NewToolExecutor(
	pool *tool.ToolPool,
	perm permission.Checker,
	logger *zap.Logger,
	timeout time.Duration,
	approvalFn PermissionApprovalFunc,
) *ToolExecutor {
	return &ToolExecutor{
		pool:        pool,
		permChecker: perm,
		logger:      logger,
		timeout:     timeout,
		approvalFn:  approvalFn,
	}
}

// SetArtifactStore sets the artifact store that will be injected into tool
// execution contexts. Tools like ArtifactGet and Write (with artifact_ref)
// use this to access stored artifacts.
func (te *ToolExecutor) SetArtifactStore(store tool.ArtifactStore) {
	te.artifactStore = store
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
		t := te.pool.Get(tc.Name)
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
	t := te.pool.Get(tc.Name)
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
	// First, check if the tool itself provides a pre-check that auto-allows.
	// This runs before the general pipeline because the pipeline's Check()
	// method does not pass the tool instance (req.Tool is nil), so
	// ToolCheckPermStep never fires. We handle it here directly.
	permSkipped := false
	if preChecker, ok := t.(tool.PermissionPreChecker); ok {
		preResult := preChecker.CheckPermission(ctx, rawInput)
		switch preResult.Behavior {
		case "allow":
			permSkipped = true
		case "deny":
			return types.ToolResult{
				Content: fmt.Sprintf("permission denied for %s: %s", tc.Name, preResult.Message),
				IsError: true,
			}
		}
	}

	if !permSkipped {
	permResult := te.permChecker.Check(ctx, tc.Name, rawInput, t.IsReadOnly())
	switch permResult.Decision {
	case permission.Deny:
		return types.ToolResult{
			Content: fmt.Sprintf("permission denied for %s: %s", tc.Name, permResult.Message),
			IsError: true,
		}
	case permission.Ask:
		// Send approval request to client and wait for response.
		if te.approvalFn == nil {
			// No approval handler — fall back to deny.
			return types.ToolResult{
				Content: fmt.Sprintf("tool %s requires approval: %s", tc.Name, permResult.Message),
				IsError: true,
			}
		}

		// Extract the fine-grained permission key (e.g. "Bash:git", "Edit:/path").
		permKey := extractPermissionKey(tc.Name, tc.Input)

		// Derive a human-readable command label for the UI.
		// "Bash:git" → "git", "Edit:/src/main.go" → "Edit /src/main.go", "Grep" → "Grep"
		cmdLabel := permKeyLabel(permKey, tc.Name)

		// Build a clear, actionable permission message for the user.
		permMessage := permResult.Message
		if permMessage == "" {
			if t.IsReadOnly() {
				permMessage = fmt.Sprintf("Allow %s to read data?", cmdLabel)
			} else {
				permMessage = fmt.Sprintf("Allow %s to make changes?", cmdLabel)
			}
		}

		// Session-scope label shows what exactly will be auto-approved.
		sessionLabel := fmt.Sprintf("Always allow %s in this session", cmdLabel)

		req := &types.PermissionRequest{
			RequestID:     "perm_" + uuid.New().String()[:8],
			ToolName:      tc.Name,
			ToolInput:     tc.Input,
			Message:       permMessage,
			IsReadOnly:    t.IsReadOnly(),
			PermissionKey: permKey,
			Options: []types.PermissionOption{
				{Label: "Allow once", Scope: types.PermissionScopeOnce, Allow: true},
				{Label: sessionLabel, Scope: types.PermissionScopeSession, Allow: true},
				{Label: "Deny", Scope: types.PermissionScopeOnce, Allow: false},
			},
		}
		resp := te.approvalFn(ctx, out, req)
		if !resp.Approved {
			msg := "user denied permission"
			if resp.Message != "" {
				msg = resp.Message
			}
			return types.ToolResult{
				Content: fmt.Sprintf("Permission denied for %s: %s", tc.Name, msg),
				IsError: true,
			}
		}
		te.logger.Info("permission approved",
			zap.String("tool", tc.Name),
			zap.String("request_id", req.RequestID),
			zap.String("scope", string(resp.Scope)),
		)
	}
	} // end if !permSkipped

	// Execute with timeout. Long-running tools (e.g., Agent) manage their own
	// timeout and bypass the executor's default.
	var execCtx context.Context
	var cancel context.CancelFunc
	if lrt, ok := t.(tool.LongRunningTool); ok && lrt.IsLongRunning() {
		execCtx, cancel = ctx, func() {}
	} else {
		execCtx, cancel = context.WithTimeout(ctx, te.timeout)
	}
	defer cancel()

	// Inject the event output channel into the context so tools that need to
	// emit events (e.g., Agent tool for subagent.start/end) can access it.
	execCtx = tool.WithEventOut(execCtx, out)

	// Inject the artifact store so tools (ArtifactGet, Write with artifact_ref)
	// can access stored artifacts.
	if te.artifactStore != nil {
		execCtx = tool.WithArtifactStore(execCtx, te.artifactStore)
	}

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

// permKeyLabel converts a permission key into a human-readable label.
//
//	"Bash:git"            → "git"
//	"Bash:npm"            → "npm"
//	"Edit:/src/main.go"   → "Edit /src/main.go"
//	"Grep"                → "Grep"
func permKeyLabel(permKey, toolName string) string {
	if idx := strings.IndexByte(permKey, ':'); idx >= 0 {
		prefix := permKey[:idx]
		suffix := permKey[idx+1:]
		if prefix == "Bash" {
			// For Bash commands, show just the program name (e.g. "git").
			return suffix
		}
		// For file tools, show "Edit /path" style.
		return prefix + " " + suffix
	}
	return toolName
}
