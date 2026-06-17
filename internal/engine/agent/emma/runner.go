package emma

import (
	"context"

	"harnessclaw-go/internal/channel/emit"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/loop/toolexec"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
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

	// MaxTurns 优先级：MainAgentMaxTurns 显式设置时一律采纳（含 0 = unlimited）；
	// 完全未设置（< 0 这种"未触碰"语义）才走 Config.MaxTurns fallback。
	//
	// emma 默认 MainAgentMaxTurns=0 → 主 agent 不被 turn 掐死，loop.Run 把 0
	// 视为无上限。要把 emma 改成有限 turn 的调用方显式传正数。
	maxTurns := cfg.MainAgentMaxTurns
	if cfg.MainAgentMaxTurns < 0 {
		maxTurns = cfg.MaxTurns
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

	// Main-agent filesystem scope: read/edit/write inside the workspace root
	// auto-allow; paths that leave the workspace trigger the per-path
	// escalation prompt wired in the tool executor (scope.go). Empty
	// WorkspaceRoot (no $HOME) leaves the scope unrestricted (legacy).
	var agentScope tool.AgentScope
	if wsRoot := workspace.DefaultRootDir(); wsRoot != "" {
		agentScope = tool.AgentScope{
			ReadScope:  []string{wsRoot},
			WriteScope: []string{wsRoot},
		}
	}

	res, err := loop.Run(ctx, &loop.Config{
		AgentScope:           agentScope,
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
		OnTurnComplete:       e.emmaMainHook(),
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
