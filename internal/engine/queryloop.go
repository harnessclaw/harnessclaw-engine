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
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/pkg/types"
)

// QueryEngineConfig holds tunables for the query loop.
type QueryEngineConfig struct {
	MaxTurns             int
	AutoCompactThreshold float64
	ToolTimeout          time.Duration
	MaxTokens            int
	SystemPrompt         string
	// ClientTools enables client-side tool execution mode.
	// When true, tool calls are sent to the client via tool_call events
	// instead of being executed server-side.
	ClientTools bool
}

// DefaultQueryEngineConfig returns production defaults.
func DefaultQueryEngineConfig() QueryEngineConfig {
	return QueryEngineConfig{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          120 * time.Second,
		MaxTokens:            16384,
		SystemPrompt:         "You are a helpful assistant.",
		ClientTools:          true,
	}
}

// pendingToolCall tracks a tool call awaiting client result.
type pendingToolCall struct {
	resultCh chan *types.ToolResultPayload
}

// pendingPermission tracks a permission request awaiting client approval.
type pendingPermission struct {
	resultCh chan *types.PermissionResponse
}

// QueryEngine is the concrete Engine implementation that runs the 5-phase query loop.
type QueryEngine struct {
	provider    provider.Provider
	registry    *tool.Registry
	cmdRegistry *command.Registry
	sessionMgr  *session.Manager
	compactor   compact.Compactor
	permChecker permission.Checker
	eventBus    *event.Bus
	logger      *zap.Logger
	config      QueryEngineConfig

	// Cached skill listing (computed once, reused per query).
	skillListing string

	// In-flight session tracking for abort support.
	mu      sync.Mutex
	cancels map[string]context.CancelFunc

	// Pending tool calls awaiting client results (client-tools mode).
	toolMu       sync.Mutex
	pendingTools map[string]*pendingToolCall // tool_use_id → pending

	// Pending permission requests awaiting client approval.
	permMu       sync.Mutex
	pendingPerms map[string]*pendingPermission // request_id → pending

	// Session-level tool allow list. When a user chooses "allow always in this session",
	// the tool name is recorded here and subsequent invocations auto-approve.
	sessionAllowMu    sync.RWMutex
	sessionAllowTools map[string]map[string]bool // session_id → tool_name → true
}

// NewQueryEngine creates a new query engine.
func NewQueryEngine(
	prov provider.Provider,
	reg *tool.Registry,
	mgr *session.Manager,
	comp compact.Compactor,
	perm permission.Checker,
	bus *event.Bus,
	logger *zap.Logger,
	cfg QueryEngineConfig,
	cmdReg *command.Registry,
) *QueryEngine {
	return &QueryEngine{
		provider:     prov,
		registry:     reg,
		cmdRegistry:  cmdReg,
		sessionMgr:   mgr,
		compactor:    comp,
		permChecker:  perm,
		eventBus:     bus,
		logger:       logger,
		config:       cfg,
		cancels:           make(map[string]context.CancelFunc),
		pendingTools:       make(map[string]*pendingToolCall),
		pendingPerms:       make(map[string]*pendingPermission),
		sessionAllowTools:  make(map[string]map[string]bool),
	}
}

// ProcessMessage implements Engine. It appends the user message to the session
// and runs the query loop, emitting events on the returned channel.
func (qe *QueryEngine) ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error) {
	// Retrieve or create the session.
	sess, err := qe.sessionMgr.GetOrCreate(ctx, sessionID, "", "")
	if err != nil {
		return nil, fmt.Errorf("session get-or-create: %w", err)
	}

	sess.AddMessage(*msg)

	// Create a cancellable context for this query.
	qCtx, cancel := context.WithCancel(ctx)

	qe.mu.Lock()
	qe.cancels[sessionID] = cancel
	qe.mu.Unlock()

	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)
		defer func() {
			qe.mu.Lock()
			delete(qe.cancels, sessionID)
			qe.mu.Unlock()
			cancel()
		}()

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryStarted,
			Payload: map[string]string{"session_id": sessionID},
		})

		terminal := qe.runQueryLoop(qCtx, sess, out)

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryCompleted,
			Payload: map[string]any{"session_id": sessionID, "reason": terminal.Reason, "message": terminal.Message},
		})

		cumUsage := qe.cumulativeUsageFor(sess.ID)
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: &terminal,
			Usage:    &cumUsage,
		}
	}()

	return out, nil
}

// SubmitToolResult implements Engine. It delivers a client-side tool result
// to the waiting query loop goroutine.
func (qe *QueryEngine) SubmitToolResult(_ context.Context, _ string, result *types.ToolResultPayload) error {
	qe.toolMu.Lock()
	pending, ok := qe.pendingTools[result.ToolUseID]
	qe.toolMu.Unlock()

	if !ok {
		return fmt.Errorf("no pending tool call for tool_use_id %s", result.ToolUseID)
	}

	select {
	case pending.resultCh <- result:
		return nil
	default:
		return fmt.Errorf("tool result channel full for %s", result.ToolUseID)
	}
}

// SubmitPermissionResult implements Engine. It delivers a permission approval/denial
// from the client to the waiting tool executor.
func (qe *QueryEngine) SubmitPermissionResult(_ context.Context, _ string, resp *types.PermissionResponse) error {
	qe.permMu.Lock()
	pending, ok := qe.pendingPerms[resp.RequestID]
	qe.permMu.Unlock()

	if !ok {
		return fmt.Errorf("no pending permission request for request_id %s", resp.RequestID)
	}

	select {
	case pending.resultCh <- resp:
		return nil
	default:
		return fmt.Errorf("permission response channel full for %s", resp.RequestID)
	}
}

// requestPermissionApproval registers a pending permission request, emits the
// event to the client, and blocks until the client responds or the context is cancelled.
// This is passed to the ToolExecutor as a callback.
//
// If the tool+command has been previously approved with scope=session for this session,
// the request is auto-approved without asking the client again.
func (qe *QueryEngine) requestPermissionApproval(
	ctx context.Context,
	out chan<- types.EngineEvent,
	sessionID string,
	req *types.PermissionRequest,
) *types.PermissionResponse {
	permKey := req.PermissionKey
	if permKey == "" {
		permKey = req.ToolName // fallback for non-Bash tools without a specific key
	}

	// Fast path: check if this tool+command is already session-approved.
	qe.sessionAllowMu.RLock()
	if tools, ok := qe.sessionAllowTools[sessionID]; ok && tools[permKey] {
		qe.sessionAllowMu.RUnlock()
		qe.logger.Debug("permission auto-approved (session scope)",
			zap.String("permission_key", permKey),
			zap.String("session_id", sessionID),
		)
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  true,
			Scope:     types.PermissionScopeSession,
			Message:   "auto-approved (session scope)",
		}
	}
	qe.sessionAllowMu.RUnlock()

	ch := make(chan *types.PermissionResponse, 1)
	qe.permMu.Lock()
	qe.pendingPerms[req.RequestID] = &pendingPermission{resultCh: ch}
	qe.permMu.Unlock()

	defer func() {
		qe.permMu.Lock()
		delete(qe.pendingPerms, req.RequestID)
		qe.permMu.Unlock()
	}()

	// Emit permission_request event to the client.
	out <- types.EngineEvent{
		Type:              types.EngineEventPermissionRequest,
		PermissionRequest: req,
	}

	// Wait for client response — block indefinitely until the user acts or
	// the session is aborted (ctx cancelled).  Permission decisions are a
	// human action; applying an artificial timeout would silently deny
	// operations the user simply hasn't reviewed yet.
	var resp *types.PermissionResponse
	select {
	case <-ctx.Done():
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  false,
			Message:   "request cancelled",
		}
	case resp = <-ch:
	}

	// If approved with session scope, record for future auto-approval.
	if resp.Approved && resp.Scope == types.PermissionScopeSession {
		qe.sessionAllowMu.Lock()
		if qe.sessionAllowTools[sessionID] == nil {
			qe.sessionAllowTools[sessionID] = make(map[string]bool)
		}
		qe.sessionAllowTools[sessionID][permKey] = true
		qe.sessionAllowMu.Unlock()
		qe.logger.Info("command session-approved",
			zap.String("permission_key", permKey),
			zap.String("session_id", sessionID),
		)
	}

	return resp
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
	case "Bash":
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

	case "Edit", "Write", "Read":
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

// AbortSession implements Engine. Cancels the in-flight query for a session.
func (qe *QueryEngine) AbortSession(_ context.Context, sessionID string) error {
	qe.mu.Lock()
	cancel, ok := qe.cancels[sessionID]
	qe.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active query for session %s", sessionID)
	}
	cancel()
	return nil
}

// --- loopState tracks mutable state across turns of the query loop. ---

type loopState struct {
	turn            int
	stopReason      string
	lastUsage       *types.Usage
	cumulativeUsage types.Usage
}

// runQueryLoop implements the 5-phase query loop modeled on TypeScript query.ts.
//
// Phase 1: Preprocess — assemble system prompt, tool schemas, compact if needed.
// Phase 2: LLM Call — stream the response, collect text + tool calls.
// Phase 3: Error Recovery — handle prompt_too_long, max_output_tokens, rate limits.
// Phase 4: Tool Execution — run requested tools (server-side or client-side).
// Phase 5: Continuation — check terminal conditions, append assistant + tool messages, loop.
func (qe *QueryEngine) runQueryLoop(ctx context.Context, sess *session.Session, out chan<- types.EngineEvent) types.Terminal {
	ls := &loopState{}

	// Build the ToolPool once per query loop from the registry.
	pool := tool.NewToolPool(qe.registry, nil /*mcpTools*/, nil /*denyRules*/)

	// Create the approval function that sends permission requests to the client via `out`.
	approvalFn := func(ctx context.Context, evtOut chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
		return qe.requestPermissionApproval(ctx, evtOut, sess.ID, req)
	}
	executor := NewToolExecutor(pool, qe.permChecker, qe.logger, qe.config.ToolTimeout, approvalFn)

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		// Auto-compact if needed.
		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, qe.config.MaxTokens, qe.config.AutoCompactThreshold) {
			qe.logger.Info("auto-compact triggered", zap.String("session_id", sess.ID), zap.Int("msg_count", len(messages)))
			qe.eventBus.Publish(event.Event{Topic: event.TopicCompactTriggered, Payload: map[string]string{"session_id": sess.ID}})

			compacted, err := qe.compactor.Compact(ctx, messages)
			if err != nil {
				qe.logger.Warn("auto-compact failed, continuing with full history", zap.Error(err))
			} else {
				// Replace session messages with compacted history.
				sess.SetMessages(compacted)
				messages = compacted
			}
		}

		// Check max turns.
		if ls.turn > qe.config.MaxTurns {
			return types.Terminal{
				Reason:  types.TerminalMaxTurns,
				Message: fmt.Sprintf("reached max turns (%d)", qe.config.MaxTurns),
				Turn:    ls.turn - 1,
			}
		}

		// Build the LLM request.
		// Inject skill listing as <system-reminder> user message (Layer 3 of 3-layer skill injection).
		// This is transient — not persisted to the session — matching TS attachment behavior.
		apiMessages := messages
		if listing := qe.getSkillListing(); listing != "" {
			skillMsg := types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: "<system-reminder>\nThe following skills are available for use with the Skill tool:\n\n" + listing + "\n</system-reminder>",
				}},
			}
			apiMessages = append([]types.Message{skillMsg}, apiMessages...)
		}

		req := &provider.ChatRequest{
			Messages:  apiMessages,
			System:    qe.config.SystemPrompt,
			Tools:     pool.Schemas(),
			MaxTokens: qe.config.MaxTokens,
		}

		// ---- Phase 2: LLM Call (streaming) ----

		// Emit message.start before streaming begins.
		msgID := "msg_" + uuid.New().String()[:8]
		out <- types.EngineEvent{
			Type:      types.EngineEventMessageStart,
			MessageID: msgID,
			Model:     qe.provider.Name(),
			Usage: &types.Usage{
				InputTokens: ls.cumulativeUsage.InputTokens,
			},
		}

		stream, err := qe.provider.Chat(ctx, req)
		if err != nil {
			// ---- Phase 3: Error Recovery (connection/auth errors) ----

			// Emit the error event so the client sees the details.
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}

			// Emit message.delta + message.stop even on error so the message lifecycle is complete.
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: err}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "query cancelled", Turn: ls.turn}
			}
			qe.logger.Error("LLM chat request failed",
				zap.String("session_id", sess.ID),
				zap.Int("turn", ls.turn),
				zap.Error(err),
			)
			return types.Terminal{Reason: types.TerminalModelError, Message: err.Error(), Turn: ls.turn}
		}

		// Collect the streamed response.
		var textBuf string
		var toolCalls []types.ToolCall

		for evt := range stream.Events {
			switch evt.Type {
			case types.StreamEventText:
				textBuf += evt.Text
				out <- types.EngineEvent{Type: types.EngineEventText, Text: evt.Text}

			case types.StreamEventToolUse:
				if evt.ToolCall != nil {
					toolCalls = append(toolCalls, *evt.ToolCall)
					// Emit tool_use content block so clients can see what the LLM requested.
					out <- types.EngineEvent{
						Type:      types.EngineEventToolUse,
						ToolUseID: evt.ToolCall.ID,
						ToolName:  evt.ToolCall.Name,
						ToolInput: evt.ToolCall.Input,
					}
				}

			case types.StreamEventMessageEnd:
				ls.stopReason = evt.StopReason
				if evt.Usage != nil {
					ls.lastUsage = evt.Usage
					ls.cumulativeUsage.InputTokens += evt.Usage.InputTokens
					ls.cumulativeUsage.OutputTokens += evt.Usage.OutputTokens
					ls.cumulativeUsage.CacheRead += evt.Usage.CacheRead
					ls.cumulativeUsage.CacheWrite += evt.Usage.CacheWrite
				}

			case types.StreamEventError:
				if evt.Error != nil {
					out <- types.EngineEvent{Type: types.EngineEventError, Error: evt.Error}
				}
			}
		}

		if streamErr := stream.Err(); streamErr != nil {
			// Emit the error event so the client sees the details.
			out <- types.EngineEvent{Type: types.EngineEventError, Error: streamErr}

			// Emit message lifecycle events to close cleanly.
			out <- types.EngineEvent{
				Type:       types.EngineEventMessageDelta,
				StopReason: "error",
				Error:      streamErr,
				Usage:      ls.lastUsage,
			}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "streaming aborted", Turn: ls.turn}
			}
			// Phase 3: treat stream errors as model errors.
			qe.logger.Error("LLM stream error",
				zap.String("session_id", sess.ID),
				zap.Int("turn", ls.turn),
				zap.Error(streamErr),
			)
			return types.Terminal{Reason: types.TerminalModelError, Message: streamErr.Error(), Turn: ls.turn}
		}

		// Emit message.delta with stop_reason and output usage.
		stopReason := ls.stopReason
		if stopReason == "" {
			if len(toolCalls) > 0 {
				stopReason = "tool_use"
			} else {
				stopReason = "end_turn"
			}
		}
		out <- types.EngineEvent{
			Type:       types.EngineEventMessageDelta,
			StopReason: stopReason,
			Usage:      ls.lastUsage,
		}

		// Emit message.stop to close this message.
		out <- types.EngineEvent{Type: types.EngineEventMessageStop}

		// Append assistant message to session.
		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): Check terminal — no tool calls means LLM is done. ----
		if len(toolCalls) == 0 {
			return types.Terminal{
				Reason:  types.TerminalCompleted,
				Message: "model finished",
				Turn:    ls.turn,
			}
		}

		// ---- Phase 4: Tool Execution ----
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled before tool execution", Turn: ls.turn}
		}

		var results []types.ToolResult

		if qe.config.ClientTools {
			// Client-side tool execution: emit tool.call events and wait for results.
			results = qe.executeClientTools(ctx, toolCalls, out)
		} else {
			// Server-side tool execution (legacy).
			results = executor.ExecuteBatch(ctx, toolCalls, out)
		}

		// Check if cancelled during tool execution.
		if ctx.Err() != nil {
			return types.Terminal{Reason: types.TerminalAbortedTools, Message: "cancelled during tool execution", Turn: ls.turn}
		}

		// Append tool result messages to session, then inject any NewMessages
		// (e.g., SkillTool injects skill prompts as user messages after tool_result).
		for i, tc := range toolCalls {
			toolMsg := types.Message{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{
						Type:       types.ContentTypeToolResult,
						ToolUseID:  tc.ID,
						ToolName:   tc.Name,
						ToolResult: results[i].Content,
						IsError:    results[i].IsError,
					},
				},
				CreatedAt: time.Now(),
			}
			sess.AddMessage(toolMsg)

			// Append NewMessages from tool result (TS newMessages pattern).
			// SkillTool uses this to inject the expanded skill prompt as a
			// user message, so the model treats it as an instruction.
			for _, nm := range results[i].NewMessages {
				sess.AddMessage(nm)
			}
		}

		// ---- Phase 5 (part B): Check stop reason. ----
		if ls.stopReason == "end_turn" && len(toolCalls) > 0 {
			// LLM said end_turn but also requested tools — continue so the LLM
			// can see tool results. This matches the TS behavior.
			continue
		}

		// Loop continues: the LLM needs to see tool results and respond again.
	}
}

// executeClientTools sends tool.call events to the client and waits for tool.result responses.
func (qe *QueryEngine) executeClientTools(
	ctx context.Context,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	results := make([]types.ToolResult, len(toolCalls))

	// Register pending tool calls.
	pendingChs := make([]chan *types.ToolResultPayload, len(toolCalls))
	for i, tc := range toolCalls {
		ch := make(chan *types.ToolResultPayload, 1)
		pendingChs[i] = ch
		qe.toolMu.Lock()
		qe.pendingTools[tc.ID] = &pendingToolCall{resultCh: ch}
		qe.toolMu.Unlock()

		// Emit tool.call event to the client.
		out <- types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		}
	}

	// Wait for all results.
	for i, tc := range toolCalls {
		select {
		case <-ctx.Done():
			results[i] = types.ToolResult{
				Content: "execution cancelled",
				IsError: true,
			}
		case payload := <-pendingChs[i]:
			results[i] = toolResultFromPayload(payload)
		case <-time.After(qe.config.ToolTimeout):
			results[i] = types.ToolResult{
				Content: fmt.Sprintf("tool %s timed out waiting for client result", tc.Name),
				IsError: true,
			}
		}

		// Clean up pending entry.
		qe.toolMu.Lock()
		delete(qe.pendingTools, tc.ID)
		qe.toolMu.Unlock()
	}

	return results
}

// toolResultFromPayload converts a client-submitted ToolResultPayload to an engine ToolResult.
func toolResultFromPayload(p *types.ToolResultPayload) types.ToolResult {
	switch p.Status {
	case "success":
		return types.ToolResult{Content: p.Output, IsError: false}
	case "error":
		return types.ToolResult{Content: p.Output + "\n" + p.ErrorMessage, IsError: true}
	case "denied":
		return types.ToolResult{
			Content: fmt.Sprintf("Permission denied: %s", p.ErrorMessage),
			IsError: true,
		}
	case "timeout":
		return types.ToolResult{
			Content: fmt.Sprintf("Execution timed out: %s", p.ErrorMessage),
			IsError: true,
		}
	case "cancelled":
		return types.ToolResult{
			Content: fmt.Sprintf("Execution cancelled: %s", p.ErrorMessage),
			IsError: true,
		}
	default:
		return types.ToolResult{Content: p.Output, IsError: p.Status != "success"}
	}
}

// cumulativeUsageFor returns a placeholder cumulative usage.
// The real implementation tracks per-session usage.
func (qe *QueryEngine) cumulativeUsageFor(_ string) types.Usage {
	return types.Usage{}
}

// buildAssistantMessage creates a Message from the LLM's streamed output.
func buildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage) types.Message {
	content := make([]types.ContentBlock, 0, 1+len(toolCalls))

	if text != "" {
		content = append(content, types.ContentBlock{
			Type: types.ContentTypeText,
			Text: text,
		})
	}

	for _, tc := range toolCalls {
		content = append(content, types.ContentBlock{
			Type:      types.ContentTypeToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		})
	}

	tokens := 0
	if usage != nil {
		tokens = usage.OutputTokens
	}

	return types.Message{
		Role:      types.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now(),
		Tokens:    tokens,
	}
}

// getSkillListing returns the cached skill listing string.
// Computed once on first call using FormatCommandsWithinBudget (lazy init).
func (qe *QueryEngine) getSkillListing() string {
	if qe.skillListing != "" {
		return qe.skillListing
	}
	if qe.cmdRegistry == nil {
		return ""
	}
	cmds := qe.cmdRegistry.GetSkillToolCommands()
	if len(cmds) == 0 {
		return ""
	}
	// Use 200k context window as default budget reference.
	qe.skillListing = skilltool.FormatCommandsWithinBudget(cmds, 200000)
	qe.logger.Info("skill listing generated for injection",
		zap.Int("skill_count", len(cmds)),
		zap.Int("listing_len", len(qe.skillListing)),
	)
	return qe.skillListing
}
