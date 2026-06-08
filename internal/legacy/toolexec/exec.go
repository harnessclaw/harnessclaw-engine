package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"harnessclaw-go/internal/legacy/sessionstats"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/tools"
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

	// artifactProducer is the identity stamp every artifact written from
	// this executor inherits. Set by the engine when constructing the
	// executor for a given session/agent.
	artifactProducer tool.ArtifactProducer
	// taskContract carries the deliverable expectations + temporal
	// boundary the submit_task_result tool reads back during M4 validation.
	// Empty contract = no enforcement; the loop terminates on plain
	// end_turn as before.
	taskContract tool.TaskContract
	// agentScope is the per-spawn filesystem scope reachable from tool
	// ctx. File* tools read it to reject out-of-scope paths. Zero-value
	// = no restriction (legacy compat).
	agentScope tool.AgentScope

	// agentID stamps every emitted ToolStart/ToolEnd event with the
	// owning agent (== card_id in the wire envelope) so the translator
	// can route tool cards under the correct sub-agent's Emitter
	// instead of falling back to the main session's emitter. Empty =
	// route via the main scope (legacy / L1 path).
	agentID string

	// statsRegistry, if non-nil, gets a RecordToolCall ping on each
	// tool_end. Set via SetStatsRegistry at construction. nil = no-op
	// (tests, ad-hoc executors).
	statsRegistry *sessionstats.Registry
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

// SetStatsRegistry wires the session metrics registry so tool_end
// increments the per-session tool-call counter.
func (te *ToolExecutor) SetStatsRegistry(r *sessionstats.Registry) {
	te.statsRegistry = r
}

// SetArtifactProducer fixes the producer identity stamp the executor
// attaches to every artifact written by tools running through it.
func (te *ToolExecutor) SetArtifactProducer(p tool.ArtifactProducer) {
	te.artifactProducer = p
}

// SetTaskContract installs the deliverable contract reachable from tool
// execution context. submit_task_result uses it to validate every claimed
// artifact_id against the parent's expectations. Pass a zero-value
// TaskContract on legacy / unrestricted dispatches.
func (te *ToolExecutor) SetTaskContract(c tool.TaskContract) {
	te.taskContract = c
}

// SetAgentScope installs the per-spawn filesystem scope reachable from
// tool execution context. File* tools enforce ReadScope/WriteScope when
// non-empty; zero-value = no restriction (legacy compat).
func (te *ToolExecutor) SetAgentScope(s tool.AgentScope) {
	te.agentScope = s
}

// SetAgentID stamps every emitted ToolStart/ToolEnd event with this id
// so the wire translator can route tool cards under the owning
// sub-agent's Emitter. Without this every sub-agent's tool calls leak
// into the main agent's stream and the UI loses tier hierarchy.
func (te *ToolExecutor) SetAgentID(id string) {
	te.agentID = id
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
	cleanInput, intent := StripIntent(tc.Input)
	tc.Input = cleanInput
	if intent != "" {
		out <- types.EngineEvent{
			Type:      types.EngineEventAgentIntent,
			AgentID:   te.agentID,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			Intent:    intent,
		}
	}

	// Emit tool_start event. AgentID lets the translator route the
	// tool card under this sub-agent's Emitter instead of the main
	// scope — without it the L2/L3 tool calls render under emma.
	out <- types.EngineEvent{
		Type:      types.EngineEventToolStart,
		AgentID:   te.agentID,
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

		// Emit tool_end event. AgentID mirrors tool_start so the
		// translator closes the tool card under the same Emitter that
		// opened it.
		evt := types.EngineEvent{
			Type:       types.EngineEventToolEnd,
			AgentID:    te.agentID,
			ToolUseID:  tc.ID,
			ToolName:   tc.Name,
			ToolResult: &result,
		}
		// Doc §10: surface produced artifacts on the wire as Refs, not
		// content. Two metadata shapes are accepted:
		//   1. metadata["artifacts"] = []ArtifactRef — used by task /
		//      scheduler tools that aggregate refs from sub-agent
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
		// Tracker hook: bump the tool-call counter on the per-session
		// tracker so the metrics dashboard reflects this invocation. We
		// read sessionID from ctx (set by ProcessMessage / SpawnSync)
		// rather than threading it through every executor call site.
		// nil-guards keep tests that don't enable stats unaffected.
		if te.statsRegistry != nil {
			if sid, ok := sessionstats.SessionIDFromCtx(ctx); ok {
				if tr := te.statsRegistry.Get(sid); tr != nil {
					tr.RecordToolCall()
				}
			}
			// Plan B dual-write: also bump the root tracker when root differs
			// from the immediate parent session so GET /sessions/{root}/metrics
			// shows aggregate tool counts across all L2/L3 descendants.
			if rootSID, ok := sessionstats.RootSessionIDFromCtx(ctx); ok {
				if sidVal, _ := sessionstats.SessionIDFromCtx(ctx); rootSID != sidVal {
					if tr := te.statsRegistry.Get(rootSID); tr != nil {
						tr.RecordToolCall()
					}
				}
			}
		}
		out <- evt

		// Server-log line per tool execution. Logged at INFO so operators
		// can watch L2/L3 lifecycle in production without flipping to
		// debug. Includes:
		//   - agent_id: which sub-agent layer ran this — lets you skim the
		//     log and see "L2 calls Task → L3 calls web_search → L3 calls
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
				Content:   fmt.Sprintf("internal error: tool %s panicked", tc.Name),
				IsError:   true,
				ErrorType: types.ToolErrorInternal,
			}
		}
	}()

	// Look up tool.
	t := te.pool.Get(tc.Name)
	if t == nil {
		return types.ToolResult{
			Content:   fmt.Sprintf("unknown tool: %s", tc.Name),
			IsError:   true,
			ErrorType: types.ToolErrorInvalidInput,
		}
	}

	// Check enabled.
	if !t.IsEnabled() {
		return types.ToolResult{
			Content:   fmt.Sprintf("tool %s is disabled", tc.Name),
			IsError:   true,
			ErrorType: types.ToolErrorPermissionDenied,
		}
	}

	// Validate input.
	rawInput := json.RawMessage(tc.Input)
	if err := t.ValidateInput(rawInput); err != nil {
		return types.ToolResult{
			Content:   wrapValidateErr(tc.Name, tc.Input, err),
			IsError:   true,
			ErrorType: types.ToolErrorInvalidInput,
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
				Content:   fmt.Sprintf("permission denied for %s: %s", tc.Name, preResult.Message),
				IsError:   true,
				ErrorType: types.ToolErrorPermissionDenied,
			}
		}
	}

	if !permSkipped {
	permResult := te.permChecker.Check(ctx, tc.Name, rawInput, t.IsReadOnly())
	switch permResult.Decision {
	case permission.Deny:
		return types.ToolResult{
			Content:   fmt.Sprintf("permission denied for %s: %s", tc.Name, permResult.Message),
			IsError:   true,
			ErrorType: types.ToolErrorPermissionDenied,
		}
	case permission.Ask:
		// Send approval request to client and wait for response.
		if te.approvalFn == nil {
			// No approval handler — fall back to deny.
			return types.ToolResult{
				Content:   fmt.Sprintf("tool %s requires approval: %s", tc.Name, permResult.Message),
				IsError:   true,
				ErrorType: types.ToolErrorPermissionDenied,
			}
		}

		// Extract the fine-grained permission key (e.g. "Bash:git", "Edit:/path").
		permKey := extractPermissionKey(tc.Name, tc.Input)

		// Derive a human-readable command label for the UI.
		// "Bash:git" → "git", "Edit:/src/main.go" → "Edit /src/main.go", "grep" → "grep"
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
			ToolUseID:     tc.ID,
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
				Content:   fmt.Sprintf("Permission denied for %s: %s", tc.Name, msg),
				IsError:   true,
				ErrorType: types.ToolErrorPermissionDenied,
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
	} else if te.timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, te.timeout)
	} else {
		// timeout <= 0 means "no executor-level cap"; tool's own
		// internal timeout (e.g. bash's defaultTimeout) still applies.
		execCtx, cancel = ctx, func() {}
	}
	defer cancel()

	// Inject the event output channel into the context so tools that need to
	// emit events (e.g., Agent tool for subagent.start/end) can access it.
	execCtx = tool.WithEventOut(execCtx, out)

	// Producer stamp travels on ctx so any tool that builds an output ref
	// (e.g. submittool's metadata round-trip) can attribute it back to
	// this spawn without each tool having to be wired with the identity.
	execCtx = tool.WithArtifactProducer(execCtx, te.artifactProducer)
	// Task contract reaches submit_task_result so M3/M4 validation can
	// match each claimed artifact against the parent's contract. Always
	// attach — when the contract is zero-value the validating tool
	// degrades to existence-only checks.
	execCtx = tool.WithTaskContract(execCtx, te.taskContract)
	// AgentScope reaches File* tools so per-spawn path restrictions are
	// enforced. Only attach when at least one field is set so empty
	// scope keeps the legacy "no restriction" behaviour observable via
	// AgentScopeFromCtx returning ok=false.
	if te.agentScope.SessionRoot != "" || len(te.agentScope.ReadScope) > 0 || len(te.agentScope.WriteScope) > 0 {
		execCtx = tool.WithAgentScope(execCtx, te.agentScope)
	}

	// ScopeEscalationFn allows file tools to prompt the user when a path
	// is outside the current read scope, rather than hard-failing silently.
	// Only injected when an approvalFn is wired (i.e. we have a live
	// session that can surface a permission card to the user).
	if te.approvalFn != nil {
		toolName := tc.Name
		toolInput := string(tc.Input)
		approvalFn := te.approvalFn
		execCtx = tool.WithScopeEscalationFn(execCtx, func(ctx context.Context, path string, isReadOnly bool) bool {
			evtOut, ok := tool.GetEventOut(ctx)
			if !ok {
				return false
			}
			var msg string
			if isReadOnly {
				msg = fmt.Sprintf("读取 %s 超出当前任务作用域，是否允许访问？", path)
			} else {
				msg = fmt.Sprintf("写入 %s 超出当前任务作用域，是否允许访问？", path)
			}
			req := &types.PermissionRequest{
				RequestID:     "perm_" + uuid.New().String()[:8],
				ToolName:      toolName,
				ToolInput:     toolInput,
				Message:       msg,
				IsReadOnly:    isReadOnly,
				PermissionKey: "ScopeRead:" + path,
				Options: []types.PermissionOption{
					{Label: "允许一次", Scope: types.PermissionScopeOnce, Allow: true},
					{Label: "本次会话始终允许", Scope: types.PermissionScopeSession, Allow: true},
					{Label: "拒绝", Scope: types.PermissionScopeOnce, Allow: false},
				},
			}
			resp := approvalFn(ctx, evtOut, req)
			return resp != nil && resp.Approved
		})
	}

	// Inject ToolUseContext so tools (e.g. scheduler) can read the
	// session ID and tool-call identity without threading these fields
	// through every call signature. Without this injection,
	// tool.GetToolUseContext always returns nil and scheduler falls
	// into the empty ParentSessionID branch — sub-agents never get
	// attributed to the parent session and metrics show sub_agents:[].
	if sid, ok := sessionstats.SessionIDFromCtx(execCtx); ok {
		tuc := &types.ToolUseContext{
			Core: types.CoreContext{
				SessionID:  sid,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolInput:  []byte(tc.Input),
			},
		}
		execCtx = tool.WithToolUseContext(execCtx, tuc)
	}

	tr, err := t.Execute(execCtx, rawInput)
	if err != nil {
		te.logger.Warn("tool execution failed",
			zap.String("tool", tc.Name),
			zap.Error(err),
		)
		// Distinguish "we ran past our deadline" from generic
		// execute errors. The former is retryable (next attempt
		// might get more time); the latter is opaque so we fall
		// back to internal. errors.Is handles wrapped DeadlineExceeded.
		errType := types.ToolErrorInternal
		if errors.Is(err, context.DeadlineExceeded) {
			errType = types.ToolErrorToolTimeout
		} else if errors.Is(err, context.Canceled) {
			errType = types.ToolErrorUserAborted
		}
		return types.ToolResult{
			Content:   fmt.Sprintf("tool %s failed: %v", tc.Name, err),
			IsError:   true,
			ErrorType: errType,
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
//	"grep"                → "grep"
func permKeyLabel(permKey, toolName string) string {
	if idx := strings.IndexByte(permKey, ':'); idx >= 0 {
		prefix := permKey[:idx]
		suffix := permKey[idx+1:]
		if prefix == "bash" {
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

// wrapValidateErr formats a ValidateInput error for the LLM. The common
// failure mode is max_tokens truncation: the assistant message was cut
// mid-stream, so the tool_call's arguments JSON is incomplete and either
// fails to unmarshal ("unexpected end of JSON input") or unmarshals into
// an empty required field. Recognise that fingerprint and replace the
// terse parser error with an actionable nudge — otherwise the LLM
// retries the same oversized write and burns another 8192 tokens.
//
// rawInput is the literal tool_input string the LLM produced; we look
// at its tail for an open quote/bracket to confirm truncation before
// rewriting the message (so callers who genuinely forgot file_path on a
// small valid JSON still get the original error).
func wrapValidateErr(toolName, rawInput string, err error) string {
	msg := err.Error()
	if isLikelyTruncation(rawInput, msg) {
		return fmt.Sprintf(
			"invalid input for %s: %v\n\n"+
				"This looks like a max_tokens (8192) truncation — your previous assistant message was cut mid-stream and the JSON arguments never finished. "+
				"DO NOT retry the same call; you will hit the same cap again. "+
				"For large file output, split it: write a minimal skeleton first, then use multiple `edit` calls to fill in each section, "+
				"or use `bash` with a heredoc to append in chunks of ≤ 1500 tokens each.",
			toolName, err,
		)
	}
	return fmt.Sprintf("invalid input for %s: %v", toolName, err)
}

// isLikelyTruncation reports whether a ValidateInput failure is most
// likely caused by max_tokens cutting the LLM's tool_input mid-stream,
// as opposed to the LLM genuinely producing wrong-shaped input.
//
// Signals (any of):
//   - parser error contains "unexpected end of JSON input"
//   - input is large (≥ 4KB — close to the per-call output budget) AND
//     ends inside an unterminated string or container; a small valid
//     JSON missing a required field is not truncation.
func isLikelyTruncation(rawInput, errMsg string) bool {
	if strings.Contains(errMsg, "unexpected end of JSON input") {
		return true
	}
	if len(rawInput) < 4096 {
		return false
	}
	trimmed := strings.TrimRight(rawInput, " \t\r\n")
	if trimmed == "" {
		return false
	}
	last := trimmed[len(trimmed)-1]
	// Properly closed JSON ends with `}` or `]`. Anything else on a
	// large input is suspicious — open quote, bare value, dangling comma.
	return last != '}' && last != ']'
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

// contractFailureSample returns up to n contract-failure strings, each
// rune-safely capped at 120 chars. Used in the engine completion log so
// operators see the actual reasons (M4 / self-check / nudge cap) rather
// than just a count. Long cascade lists get truncated to keep log lines
// scannable.
func contractFailureSample(failures []string, n int) []string {
	limit := n
	if len(failures) < limit {
		limit = len(failures)
	}
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = truncateForLog(failures[i], 120)
	}
	return out
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
func StripIntent(raw string) (string, string) {
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

// extractPermissionKey derives a fine-grained key for session-level approval.
//
// For Bash: parses the command field and extracts "program + subcommand",
// e.g. input `{"command":"git status"}` → key "Bash:git status".
// This ensures approving "git status" doesn't auto-approve "git push".
//
// For file tools (Edit/Write/Read): extracts the file_path,
// e.g. input `{"file_path":"/src/main.go",...}` → key "Edit:/src/main.go".
//
// For other tools: returns the tool name as-is.
func extractPermissionKey(toolName, toolInput string) string {
	switch toolName {
	case "bash":
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(toolInput), &input); err == nil && input.Command != "" {
			cmdID := extractCommandIdentity(input.Command)
			if cmdID != "" {
				return "Bash:" + cmdID
			}
		}
		return toolName

	case "edit", "write", "read":
		var input struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal([]byte(toolInput), &input); err == nil && input.FilePath != "" {
			return toolName + ":" + input.FilePath
		}
		return toolName

	default:
		return toolName
	}
}

// extractCommandIdentity extracts "program + subcommand" from a shell command.
// The result is used as the session-level approval key.
//
// It returns the program name plus the first non-flag token (subcommand),
// so that different subcommands require separate approval:
//
//	"git status"                → "git status"
//	"git push --force"          → "git push"
//	"git add file.go"           → "git add"
//	"sudo npm install foo"      → "npm install"
//	"ENV=val go build ./..."    → "go build"
//	"rm -rf /tmp"               → "rm"
//	"ls -la"                    → "ls"
//	"cd /tmp && make test"      → "make test"
//	"cat foo | grep bar"        → "cat"
//	"docker compose up -d"      → "docker compose"
func extractCommandIdentity(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}

	// Split on pipes/chains — take the first segment.
	for _, sep := range []string{"&&", "||", "|", ";"} {
		if idx := strings.Index(cmd, sep); idx >= 0 {
			cmd = strings.TrimSpace(cmd[:idx])
			break
		}
	}

	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return ""
	}

	// Skip leading env-var assignments (FOO=bar) and sudo/env/nohup wrappers.
	for len(tokens) > 0 {
		t := tokens[0]
		if strings.Contains(t, "=") && !strings.HasPrefix(t, "-") {
			tokens = tokens[1:]
			continue
		}
		if t == "sudo" || t == "env" || t == "nohup" || t == "nice" || t == "time" {
			tokens = tokens[1:]
			continue
		}
		break
	}

	if len(tokens) == 0 {
		return ""
	}

	// First token is the program name (strip path: /usr/bin/git → git).
	program := tokens[0]
	if idx := strings.LastIndex(program, "/"); idx >= 0 {
		program = program[idx+1:]
	}

	// Look for a subcommand: the first token after the program that is
	// neither a flag (starts with -) nor looks like a file path.
	for _, t := range tokens[1:] {
		if strings.HasPrefix(t, "-") {
			continue // flag
		}
		if looksLikePath(t) {
			break // argument, not subcommand
		}
		// Found a subcommand token.
		return program + " " + t
	}

	return program
}

// looksLikePath returns true if the token appears to be a file path or glob
// rather than a subcommand name.
func looksLikePath(t string) bool {
	return strings.HasPrefix(t, "/") ||
		strings.HasPrefix(t, "./") ||
		strings.HasPrefix(t, "../") ||
		strings.HasPrefix(t, "~") ||
		strings.Contains(t, "/") ||
		strings.HasPrefix(t, "*.") ||
		strings.HasPrefix(t, ".")
}
