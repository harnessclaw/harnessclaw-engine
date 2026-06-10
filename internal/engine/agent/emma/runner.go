package emma

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel/emit"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/legacy/toolexec"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// run drives the L1 query loop. After the loop-migration refactor this
// is a thin shell around loop.Run; the original 5-phase implementation
// (sysPrompt build, request dump, CallLLM, dispatchToolBatch, ...) all
// moved into internal/engine/loop. emma reaches into the loop through
// Hooks (observation) + OnTurnComplete (control flow).
//
// Responsibilities that stay here:
//   - Build the static system prompt (one-off per ProcessMessage)
//   - Build the tool pool + ArtifactProducer
//   - Register / deregister the mailbox for async sub-agent notifications
//   - Resolve effective MaxTurns
//   - Wire the four emma hooks + the TurnHook
//   - Reclassify the LLM-error Terminal Reason when ctx was already
//     cancelled (loop.Run reports ModelError unconditionally; legacy
//     behaviour was AbortedStreaming on ctx-cancel)
func (e *Engine) run(
	ctx context.Context,
	sess *session.Session,
	out chan<- types.EngineEvent,
	approvalFn toolexec.PermissionApprovalFunc,
) types.Terminal {
	cfg := e.config

	// Register a mailbox so async sub-agents can send completion
	// notifications back. Deregister on exit. The mailbox is read in
	// emmaMainHook when the LLM produces no tool calls.
	var mailbox *agent.Mailbox
	if broker := e.messageBroker; broker != nil {
		mailbox = broker.Register(sess.ID, "")
		defer broker.Unregister(sess.ID)
	}

	// Tool pool: optionally filtered by MainAgentAllowedTools.
	pool := tool.NewToolPool(e.registry, nil, nil)
	if len(cfg.MainAgentAllowedTools) > 0 {
		pool = pool.FilterByNames(cfg.MainAgentAllowedTools)
	}

	// Static system prompt: built once at the start of this query and
	// reused for every loop turn. The legacy code re-ran
	// buildSystemPrompt each turn and relied on session.PromptCache to
	// fake stability; building once removes byte-drift cache misses on
	// the Anthropic side.
	sysPrompt := e.buildSystemPrompt(ctx, sess, sess.GetMessages())

	// MaxTurns precedence: per-spawn MainAgentMaxTurns > MaxTurns.
	maxTurns := cfg.MaxTurns
	if cfg.MainAgentMaxTurns > 0 {
		maxTurns = cfg.MainAgentMaxTurns
	}

	// Artifact producer stamp — emma-side tools that produce artifacts
	// (rare on the main agent itself) inherit this trace_id.
	producer := tool.ArtifactProducer{
		AgentID:   "main",
		SessionID: sess.ID,
	}
	if tc := emit.FromContext(ctx); tc != nil {
		producer.TraceID = tc.TraceID
	}

	ls := &emmaLoopState{}

	res, err := loop.Run(ctx, &loop.Config{
		Session:              sess,
		SystemPrompt:         sysPrompt,
		Tools:                pool,
		Provider:             e.provider,
		Compactor:            e.compactor,
		Retryer:              e.retryer,
		Logger:               e.logger,
		MaxTurns:             maxTurns,
		MaxTokens:            cfg.MaxTokens,
		ContextWindow:        e.contextWindow(),
		ToolTimeout:          cfg.ToolTimeout,
		LLMAPITimeout:        cfg.LLMAPITimeout,
		LLMFirstByteTimeout:  cfg.LLMFirstByteTimeout,
		AutoCompactThreshold: cfg.AutoCompactThreshold,
		ArtifactProducer:     producer,
		Out:                  out,
		AgentID:              "main",
		PermChecker:          e.permChecker,
		ApprovalFn:           approvalFn,
		Hooks:                e.emmaHooks(ctx, sess, out, ls, e.provider),
		OnTurnComplete:       e.emmaMainHook(ctx, sess, mailbox, out),
	})
	if err != nil {
		// loop.Run only returns non-nil error on Config-precondition
		// failure (OnTurnComplete nil, MaxTurns ≤ 0, PermChecker nil).
		// That's a programming error — surface as ModelError.
		return types.Terminal{
			Reason:  types.TerminalModelError,
			Message: err.Error(),
		}
	}

	// ctx-aware Terminal reclassification: legacy runner returned
	// TerminalAbortedStreaming when an LLM error fired while ctx was
	// already cancelled (user pressed stop / abort endpoint), and
	// TerminalModelError when ctx was still live (upstream / network
	// failure). loop.Run doesn't make this distinction, so we rewrite
	// the Reason here when ctx is cancelled to keep the wire protocol
	// stable for downstream consumers.
	terminal := res.Terminal
	if terminal.Reason == types.TerminalModelError && ctx.Err() != nil {
		terminal.Reason = types.TerminalAbortedStreaming
		if terminal.Message == "" {
			terminal.Message = "query cancelled"
		}
	}
	return terminal
}

// shouldWaitForAsyncAgents returns true if the query loop should block on
// the mailbox instead of terminating.
func (e *Engine) shouldWaitForAsyncAgents(sessionID string, mailbox *agent.Mailbox) bool {
	if mailbox == nil {
		return false
	}
	if e.agentRegistry == nil {
		return false
	}
	return e.agentRegistry.HasRunningForParent(sessionID)
}

// waitForMailboxMessage blocks until a message arrives on the mailbox or
// the context is cancelled.
func (e *Engine) waitForMailboxMessage(
	ctx context.Context,
	sessionID string,
	mailbox *agent.Mailbox,
	out chan<- types.EngineEvent,
) *types.Message {
	_ = out
	if mailbox == nil {
		return nil
	}

	e.logger.Info("waiting for async agent notifications",
		zap.String("session_id", sessionID),
	)

	select {
	case <-ctx.Done():
		return nil
	case agentMsg, ok := <-mailbox.Receive():
		if !ok || agentMsg == nil {
			return nil
		}
		e.logger.Info("received async agent notification",
			zap.String("session_id", sessionID),
			zap.String("from", agentMsg.From),
			zap.String("type", string(agentMsg.Type)),
		)

		text := fmt.Sprintf("[Agent notification from %s]\n%s", agentMsg.From, agentMsg.Content)
		msg := &types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: text,
			}},
			CreatedAt: time.Now(),
		}
		return msg
	}
}

// cumulativeUsageFor returns the running token totals for sessionID by
// reading the sessionstats Tracker. Falls back to a zero Usage when stats
// are not wired in (tests).
func (e *Engine) cumulativeUsageFor(sessionID string) types.Usage {
	if e == nil {
		return types.Usage{}
	}
	if e.statsRegistry == nil {
		return types.Usage{}
	}
	tr := e.statsRegistry.Get(sessionID)
	if tr == nil {
		return types.Usage{}
	}
	s := tr.Snapshot()
	return types.Usage{
		InputTokens:    int(s.InputTokens),
		OutputTokens:   int(s.OutputTokens),
		CacheRead:      int(s.CacheReadTokens),
		CacheWrite:     int(s.CacheWriteTokens),
		ThinkingTokens: int(s.ThinkingTokens),
	}
}
