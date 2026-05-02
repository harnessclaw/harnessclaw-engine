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
	pool        *tool.ToolPool
	permChecker permission.Checker
	logger      *zap.Logger
	timeout     time.Duration
	approvalFn  PermissionApprovalFunc // nil = deny on Ask (legacy behavior)

	// artifactStore is the *artifact.Store the artifact tools read/write
	// from. Stored as `any` so this layer doesn't import the artifact
	// package — keeping the engine→artifact dependency one-way clean.
	artifactStore any
	// artifactProducer is the identity stamp every artifact written from
	// this executor inherits. Set by the engine when constructing the
	// executor for a given session/agent.
	artifactProducer tool.ArtifactProducer
	// taskContract carries the deliverable expectations + temporal
	// boundary the SubmitTaskResult tool reads back during M4 validation.
	// Empty contract = no enforcement; the loop terminates on plain
	// end_turn as before.
	taskContract tool.TaskContract
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

// SetArtifactStore wires the artifact store the executor injects into
// each tool's context. Pass an *artifact.Store; the executor stores it
// untyped to avoid the import cycle.
func (te *ToolExecutor) SetArtifactStore(store any) {
	te.artifactStore = store
}

// SetArtifactProducer fixes the producer identity stamp the executor
// attaches to every artifact written by tools running through it.
func (te *ToolExecutor) SetArtifactProducer(p tool.ArtifactProducer) {
	te.artifactProducer = p
}

// SetTaskContract installs the deliverable contract reachable from tool
// execution context. SubmitTaskResult uses it to validate every claimed
// artifact_id against the parent's expectations. Pass a zero-value
// TaskContract on legacy / unrestricted dispatches.
func (te *ToolExecutor) SetTaskContract(c tool.TaskContract) {
	te.taskContract = c
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
	// Strip the framework-required `intent` field before doing anything
	// else. ToolPool.Schemas injected `intent` into every tool's input
	// schema so the model is forced to fill it; here we lift it out so
	// (a) the user gets a real-time progress sentence as the call begins,
	// and (b) the underlying tool's own input schema stays unaware of
	// the convention. If the model didn't supply intent (validation
	// would normally have rejected it, but providers sometimes relax
	// constraints), we still execute — silence is better than a hard fail.
	cleanInput, intent := stripIntent(tc.Input)
	tc.Input = cleanInput
	if intent != "" {
		out <- types.EngineEvent{
			Type:      types.EngineEventAgentIntent,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			Intent:    intent,
		}
	}

	// Emit tool_start event.
	out <- types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolUseID: tc.ID,
		ToolName:  tc.Name,
		ToolInput: tc.Input,
	}

	// Track wall-clock duration so the tool_end log carries it. This is
	// the load-bearing observability hook — without it the server logs
	// only show LLM lifecycle, never which tool ran in which sub-agent
	// and how long each took. Duration goes on the structured log only;
	// the wire event already carries timestamps via the envelope.
	startedAt := time.Now()

	defer func() {
		dur := time.Since(startedAt)

		// Emit tool_end event.
		evt := types.EngineEvent{
			Type:       types.EngineEventToolEnd,
			ToolUseID:  tc.ID,
			ToolName:   tc.Name,
			ToolResult: &result,
		}
		// Doc §10: surface produced artifacts on the wire as Refs, not
		// content. Two metadata shapes are accepted:
		//   1. metadata["artifacts"] = []ArtifactRef — used by Task /
		//      Specialists tools that aggregate refs from sub-agent
		//      submissions. Lift the list directly.
		//   2. render_hint=artifact + scalar fields — used by per-call
		//      ArtifactWrite. Build a single Ref via the helper.
		// First-shape wins so dispatch tools' aggregated lists aren't
		// downgraded to a single-Ref view.
		if list, ok := result.Metadata["artifacts"].([]types.ArtifactRef); ok && len(list) > 0 {
			evt.Artifacts = list
		} else if ref, ok := artifactRefFromMetadata(result.Metadata); ok {
			evt.Artifacts = []types.ArtifactRef{ref}
		}
		out <- evt

		// Server-log line per tool execution. Logged at INFO so operators
		// can watch L2/L3 lifecycle in production without flipping to
		// debug. Includes:
		//   - agent_id: which sub-agent layer ran this — lets you skim the
		//     log and see "L2 calls Task → L3 calls WebSearch → L3 calls
		//     ArtifactWrite → L2 calls ArtifactRead" without correlation.
		//   - artifact_id: when the tool produced one, so the §10 chain
		//     (write → emit → store → read) is traceable from the log alone.
		fields := []zap.Field{
			zap.String("tool", tc.Name),
			zap.String("tool_use_id", tc.ID),
			zap.String("agent_id", te.artifactProducer.AgentID),
			zap.Duration("duration", dur),
			zap.Bool("is_error", result.IsError),
		}
		if len(evt.Artifacts) > 0 {
			fields = append(fields,
				zap.String("artifact_id", evt.Artifacts[0].ArtifactID),
				zap.String("artifact_name", evt.Artifacts[0].Name),
			)
		}
		// On error, surface the result content (truncated) so operators
		// can debug from the log without re-running with --debug. Without
		// this, "tool executed is_error=true" is a dead-end log line —
		// you have to grep upstream events or attach a debugger to find
		// out why a tool failed. The truncation keeps long stack traces /
		// validation lists from blowing the log line size.
		if result.IsError {
			snippet := result.Content
			if len(snippet) > 500 {
				snippet = snippet[:500] + "...[truncated]"
			}
			fields = append(fields,
				zap.String("error_content", snippet),
				zap.String("tool_input", truncateForLog(tc.Input, 300)),
			)
		}
		te.logger.Info("tool executed", fields...)
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

	// Inject the artifact store + producer stamp so ArtifactRead /
	// ArtifactWrite can run without each tool needing its own wiring.
	if te.artifactStore != nil {
		execCtx = tool.WithArtifactStoreValue(execCtx, te.artifactStore)
	}
	execCtx = tool.WithArtifactProducer(execCtx, te.artifactProducer)
	// Task contract reaches SubmitTaskResult so M3/M4 validation can
	// match each claimed artifact against the parent's contract. Always
	// attach — when the contract is zero-value the validating tool
	// degrades to existence-only checks.
	execCtx = tool.WithTaskContract(execCtx, te.taskContract)

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

// artifactRefFromMetadata builds a wire-shape ArtifactRef from a tool's
// result metadata. Returns false when the metadata does not describe an
// artifact (i.e. render_hint is missing or set to anything other than
// "artifact") — keeping the test there means tools that don't deal with
// artifacts cost zero allocation in the common path.
//
// Field names must stay in lock-step with the artifact write tool's
// metadata shape (internal/tool/artifacttool/write.go). The tool's
// comment block points back here.
func artifactRefFromMetadata(meta map[string]any) (types.ArtifactRef, bool) {
	if meta == nil {
		return types.ArtifactRef{}, false
	}
	hint, _ := meta["render_hint"].(string)
	if hint != "artifact" {
		return types.ArtifactRef{}, false
	}
	id, _ := meta["artifact_id"].(string)
	if id == "" {
		// render_hint says artifact but no ID — refuse to emit a partial
		// Ref. This protects clients from receiving entries they can't
		// dereference (which would look like data corruption from the UI).
		return types.ArtifactRef{}, false
	}
	ref := types.ArtifactRef{ArtifactID: id}
	if v, ok := meta["name"].(string); ok {
		ref.Name = v
	}
	if v, ok := meta["type"].(string); ok {
		ref.Type = v
	}
	if v, ok := meta["mime_type"].(string); ok {
		ref.MIMEType = v
	}
	if v, ok := meta["description"].(string); ok {
		ref.Description = v
	}
	if v, ok := meta["preview_text"].(string); ok {
		ref.PreviewText = v
	}
	if v, ok := meta["uri"].(string); ok {
		ref.URI = v
	}
	// Size may arrive as int or float64 depending on JSON round-trip path.
	switch v := meta["size"].(type) {
	case int:
		ref.SizeBytes = v
	case int64:
		ref.SizeBytes = int(v)
	case float64:
		ref.SizeBytes = int(v)
	}
	return ref, true
}

// truncateForLog clips s to at most n bytes (rune-safe) for log output.
// Avoids dumping entire 50KB tool inputs into a log line when the error
// snippet just needs the first ~few hundred chars to reveal the cause.
func truncateForLog(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	// Drop trailing partial rune if we sliced mid-codepoint.
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + "...[truncated]"
}

// stripIntent extracts and removes the `intent` field from a JSON tool
// input. Returns (cleaned JSON, intent text). If the input is not a JSON
// object or has no intent field, the input is returned unchanged with an
// empty intent — the caller treats empty intent as "model didn't fill it"
// and degrades gracefully (no progress event but the tool still runs).
//
// We marshal back via map iteration so this is robust to unknown extra
// fields the tool's own schema might have. Note that JSON object key
// order is not preserved across this round-trip — fine for tool inputs
// since Go's json package treats object property order as insignificant.
func stripIntent(raw string) (string, string) {
	if raw == "" {
		return raw, ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		// Not an object (could be array/scalar/malformed). Leave it alone.
		return raw, ""
	}
	intentRaw, ok := m[tool.IntentFieldName]
	if !ok {
		return raw, ""
	}
	delete(m, tool.IntentFieldName)

	var intent string
	if err := json.Unmarshal(intentRaw, &intent); err != nil {
		// Field present but not a string — pass through, no progress.
		return raw, ""
	}

	cleaned, err := json.Marshal(m)
	if err != nil {
		// Should never happen on a freshly-decoded map, but be safe.
		return raw, intent
	}
	return string(cleaned), strings.TrimSpace(intent)
}
