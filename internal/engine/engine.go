// Package engine implements the QueryEngine — the orchestrator that drives
// each user turn through the LLM, dispatches tool calls, manages sub-agent
// lifecycle, and emits engine events.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/sections"
	"harnessclaw-go/internal/engine/queryloop"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// Engine processes user messages through the LLM query loop.
type Engine interface {
	// ProcessMessage handles a user message for the given session,
	// returning a channel of streaming events.
	ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error)

	// SubmitToolResult delivers a client-side tool execution result back to
	// the engine so the query loop can continue with the next LLM turn.
	SubmitToolResult(ctx context.Context, sessionID string, result *types.ToolResultPayload) error

	// SubmitPermissionResult delivers a permission approval/denial response
	// from the client to the waiting tool executor.
	SubmitPermissionResult(ctx context.Context, sessionID string, resp *types.PermissionResponse) error

	// SubmitPlanResponse delivers the user's response to a PlanProposal.
	// The waiting PlanCoordinator unblocks with the (possibly edited)
	// plan or a rejection. Returns ErrNotFound when plan_id doesn't
	// match any in-flight proposal — channel adapters log the warn but
	// don't crash the connection.
	SubmitPlanResponse(ctx context.Context, sessionID string, resp *types.PlanResponse) error

	// SubmitStepDecision delivers the user's continue/retry/cancel reply
	// to a step_decision_requested prompt. Unknown request_id is logged
	// as warn and returned as nil (stale reply, not an error).
	SubmitStepDecision(ctx context.Context, sessionID string, resp *types.StepDecisionResponse) error

	// AbortSession cancels any in-flight processing for a session.
	AbortSession(ctx context.Context, sessionID string) error
}

// QueryEngineConfig holds tunables for the query loop.
type QueryEngineConfig struct {
	MaxTurns             int
	AutoCompactThreshold float64
	ToolTimeout          time.Duration
	// MaxTokens caps the response length on each Chat() call (passed
	// through as ChatRequest.MaxTokens). 0 = let the bifrost adapter
	// fall back to its agent/endpoint-resolved default. Sourced from
	// cfg.Agent.MaxTokens.
	MaxTokens int
	// ContextWindow is the conversation-level token budget the
	// compactor watches; auto-compact fires when accumulated message
	// tokens exceed ContextWindow × AutoCompactThreshold. Sourced
	// from cfg.Agent.ContextWindow with a 200_000 fallback for
	// modern provider defaults (anthropic / openai / gemini 200k).
	// Distinct from MaxTokens — previously both lived in MaxTokens,
	// which mixed "response cap" and "context budget" semantics.
	ContextWindow int
	SystemPrompt  string
	// ClientTools enables client-side tool execution mode.
	// When true, tool calls are sent to the client via tool_call events
	// instead of being executed server-side.
	ClientTools bool

	// MainAgentProfile is the prompt profile used for the user-facing main
	// agent (the one invoked via ProcessMessage). Sub-agents resolve their
	// own profile via SpawnConfig.SubagentType — this field is for the
	// non-spawn path only. When nil, falls back to WorkerProfile, which is
	// safe but generic; production deployments should always inject this.
	MainAgentProfile *prompt.AgentProfile

	// MainAgentDisplayName is the friendly leader name interpolated into
	// worker identity prompts (e.g., "你叫小林，是 emma 团队的搭档"). Empty
	// disables the substitution and keeps a generic worker identity.
	MainAgentDisplayName string

	// MainAgentAllowedTools restricts the tools advertised to the main
	// agent's LLM. Empty means no restriction (all enabled tools visible).
	// L1Engine sets this to ["Agent","orchestrate"] so emma can only delegate.
	MainAgentAllowedTools []string

	// MainAgentMaxTurns overrides MaxTurns for the user-facing main agent
	// loop only. When > 0, the main loop terminates after this many turns;
	// sub-agents continue to derive their cap from MaxTurns. L1Engine
	// typically sets this to 10 to enforce a "small" L1 loop.
	MainAgentMaxTurns int

	// MaxPlanReplans bounds PlanCoordinator re-plan attempts. Zero means
	// "use defaultMaxPlanReplans" (3). Sourced from
	// config.EngineConfig.MaxPlanReplans at engine construction.
	MaxPlanReplans int

	// MaxStepAttempts bounds Scheduler retries per transient step
	// failure. Zero means "use defaultStepMaxAttempts" (3). Sourced from
	// config.EngineConfig.MaxStepAttempts at engine construction.
	MaxStepAttempts int

	// LLMMaxRetries overrides retry.DefaultConfig().MaxRetries when > 0.
	// Sourced from config.LLM.MaxRetries. Zero falls through to the
	// retry package default (10). Each retry adds exponential backoff
	// (500ms, 1s, 2s, ... capped at 32s) with ±25% jitter.
	LLMMaxRetries int

	// LLMAPITimeout caps total wall-clock for ONE LLM call (Chat() +
	// stream consumption). When the upstream gateway holds the HTTP
	// connection open without bytes, this is what eventually times out
	// the call so retry can kick in. Zero = no total deadline (the
	// stream can run forever — used in tests; never recommended in
	// prod). Sourced from config.LLM.APITimeout.
	LLMAPITimeout time.Duration

	// LLMFirstByteTimeout caps how long we wait between Chat() returning
	// and the FIRST stream chunk arriving. Catches the "TCP black hole"
	// scenario where the model gateway accepts the request but never
	// sends a single byte — without this, the call sits silent until
	// the orphan watchdog fires (10 min later, surfacing as a useless
	// "execute timeout" error). Once the first chunk arrives this
	// timer disarms, so legitimate slow streams are unaffected.
	// Recommended: 60–120s. Zero = disabled (rely on LLMAPITimeout
	// alone).
	LLMFirstByteTimeout time.Duration

	// DisableStepDecisionGate, when true, suppresses the user-decision
	// prompt that the Scheduler / PlanCoordinator would otherwise emit
	// after a step / plan-level failure. Failure handling falls back to
	// the legacy "skip + aggregate partial result" behaviour.
	//
	// Production keeps this false: any failure pauses the run and asks
	// the user to choose continue / retry / cancel, so the user is in
	// control of the sunk-cost trade-off. Tests that don't have a
	// client to answer the prompt set this to true to avoid hanging on
	// the unanswered request.
	DisableStepDecisionGate bool

	// DefRegistry, when non-nil, enables @-mention parsing for routing
	// user messages to specialized agents. nil disables mention routing.
	DefRegistry *agent.AgentDefinitionRegistry

	// SkillReader, when non-nil, enables runtime skill discovery for
	// search_skill / load_skill tools. nil disables them.
	SkillReader *skill.Reader

	// StatsRegistry, when non-nil, lets the engine attribute LLM /
	// sub-agent / tool activity to the correct Tracker. nil disables stats wiring.
	StatsRegistry *sessionstats.Registry
}

// QueryEngine is the concrete Engine implementation that runs the 5-phase query loop.
type QueryEngine struct {
	provider     provider.Provider
	registry     *tool.Registry
	cmdRegistry  *command.Registry
	sessionMgr   *session.Manager
	compactor    compact.Compactor
	permChecker  permission.Checker
	eventBus     *event.Bus
	logger       *zap.Logger
	config       QueryEngineConfig

	// retryer drives the per-LLM-call retry loop with exponential
	// backoff + jitter + 529-fallback signalling. Shared across every
	// LLM call (main loop + sub-agents) so the consecutive-529
	// counter accumulates session-wide; that's intentional —
	// connections to the same gateway share the same upstream health.
	retryer *retry.Retryer

	// Prompt builder for structured system prompt assembly.
	promptBuilder *prompt.Builder
	promptProfile *prompt.AgentProfile

	// loopRunner owns the prompt-building helpers (and, eventually, the
	// whole runQueryLoop). Constructed once after the spawn/llmcall/
	// toolexec wiring lands so QE fully satisfies queryloop.Deps.
	loopRunner *queryloop.Runner

	// In-flight session tracking for abort support.
	mu      sync.Mutex
	cancels map[string]context.CancelFunc

	// Multi-agent support fields.
	agentRegistry  *agent.AgentRegistry
	messageBroker  *agent.MessageBroker
	defRegistry    *agent.AgentDefinitionRegistry
	mentionParser  *queryloop.MentionParser

	// skillReader provides runtime skill discovery for search_skill / load_skill
	// tools (used by freelancer L3). nil disables those tools at runtime.
	skillReader *skill.Reader

	// spawner owns the sub-agent lifecycle: taskRegistry, searchGapDetector,
	// and the 14-step SpawnSync pipeline. Constructed at engine init from
	// the QE itself (QE implements spawn.Deps).
	spawner *spawn.Spawner

	// emitSeq dispenses per-trace sequence numbers for the emit envelope.
	// Backed by sync.Map internally so concurrent traces don't contend on a
	// global mutex.
	emitSeq *emit.Sequencer

	// statsRegistry, when non-nil, lets the engine read cumulative stats
	// (see cumulativeUsageFor) and lets sub-agent hooks/tool hooks ping
	// the right Tracker. Set via SetStatsRegistry from cmd/server/main.go;
	// tests that don't enable metrics leave it nil.
	statsRegistry *sessionstats.Registry

	// schedulerCoord is the L2 scheduler.Coordinator instance.
	schedulerCoord *enginesched.Coordinator
}

// retryConfigFromEngineCfg builds a *retry.Config from the engine
// config, applying the package-level defaults for any field the engine
// doesn't explicitly override. Centralised so NewQueryEngine and tests
// stay in sync on what "default retry policy" means.
func retryConfigFromEngineCfg(cfg QueryEngineConfig) *retry.Config {
	c := retry.DefaultConfig()
	if cfg.LLMMaxRetries > 0 {
		c.MaxRetries = cfg.LLMMaxRetries
	}
	return c
}

// DefaultQueryEngineConfig returns production defaults.
func DefaultQueryEngineConfig() QueryEngineConfig {
	return QueryEngineConfig{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          120 * time.Second,
		MaxTokens:             16384,
		ContextWindow:         200000,
		SystemPrompt:          "You are a helpful assistant.",
		ClientTools:           true,
		MaxPlanReplans:        3,
		MaxStepAttempts:       3,
		LLMAPITimeout:         600 * time.Second, // 10 min total per call
		LLMFirstByteTimeout:   120 * time.Second, // upstream-stall guard
	}
}

// plannerModel returns the LLM model the LLMPlanner should call.
// Provider may expose a default model via Name() or — until we wire a
// dedicated config field — we let the provider pick (empty string).
// Most providers (Bifrost / Anthropic / OpenAI adapters) interpret an
// empty Model as "use the configured default", which matches what we
// want here: the planner inherits whatever main model the operator set.
func (qe *QueryEngine) plannerModel() string {
	// Operators wanting a separate (cheaper) planning model should plug
	// in a custom Planner via SharedDeps. Default = same model as the
	// main turn, which keeps behaviour consistent with the previous
	// HeuristicPlanner (which had no LLM cost at all).
	return ""
}

// contextWindow returns the effective auto-compact budget — the
// operator-configured ContextWindow when > 0, else a 200_000-token
// fallback aligned with modern provider defaults (claude 4.x /
// gpt-5 / gemini 2.x all sit at 200k). Centralised so subagent +
// queryloop paths never disagree.
func (qe *QueryEngine) contextWindow() int {
	return effectiveContextWindow(qe.config.ContextWindow)
}

// effectiveContextWindow resolves the operator value with the
// production fallback. Used by both QueryEngine and the sub-agent
// loop (sub-agents inherit the parent ContextWindow, so the same
// resolver applies).
func effectiveContextWindow(configured int) int {
	if configured > 0 {
		return configured
	}
	return 200000
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
	// Initialize prompt builder with default registry and built-in sections
	promptRegistry := prompt.NewRegistry()
	promptRegistry.Register(sections.NewCurrentDateSection())
	promptRegistry.Register(sections.NewRoleSection())
	// IdentitySection not registered — it's an internal detail of RoleSection.
	promptRegistry.Register(sections.NewTeamSection())
	promptRegistry.Register(sections.NewPrinciplesSection())
	promptRegistry.Register(sections.NewToolsSection())
	promptRegistry.Register(sections.NewEnvSection())
	promptRegistry.Register(sections.NewMemorySection())
	// TODO(phase2): register SkillsSection in profiles that need it.
	promptRegistry.Register(sections.NewSkillsSection())
	promptRegistry.Register(sections.NewTaskSection())
	promptBuilder := prompt.NewBuilder(promptRegistry, logger)

	// Main-agent profile is supplied via QueryEngineConfig. Falling back to
	// WorkerProfile keeps the engine generic — the L1Engine wrapper is
	// responsible for plugging in the user-facing profile (emma).
	promptProfile := cfg.MainAgentProfile
	if promptProfile == nil {
		promptProfile = prompt.WorkerProfile
	}

	qe := &QueryEngine{
		provider:          prov,
		registry:          reg,
		cmdRegistry:       cmdReg,
		sessionMgr:        mgr,
		compactor:         comp,
		permChecker:       perm,
		eventBus:          bus,
		logger:            logger,
		config:            cfg,
		promptBuilder:     promptBuilder,
		promptProfile:     promptProfile,
		cancels:           make(map[string]context.CancelFunc),
		emitSeq:           emit.NewSequencer(),
		retryer:           retry.New(retryConfigFromEngineCfg(cfg), logger),
	}

	// Apply optional config-injected dependencies. Replaces the previous
	// post-construction SetX() calls. Order matters only for defRegistry
	// (the mention parser depends on it).
	if cfg.DefRegistry != nil {
		qe.defRegistry = cfg.DefRegistry
		qe.mentionParser = queryloop.NewMentionParser(cfg.DefRegistry)
	}
	if cfg.SkillReader != nil {
		qe.skillReader = cfg.SkillReader
	}
	if cfg.StatsRegistry != nil {
		qe.statsRegistry = cfg.StatsRegistry
	}

	// Spawner depends on QE (via spawn.Deps), so it must be constructed
	// after the optional config-injected deps land — DefRegistry,
	// SkillReader, StatsRegistry are read through Deps from the spawner.
	qe.spawner = spawn.NewSpawner(qe)

	// loopRunner needs QE to fully satisfy queryloop.Deps, which
	// includes the spawn handle just constructed above. Wired here so
	// the buildSystemPrompt / getSkillListing wrappers below can dispatch
	// to the runner from day one.
	qe.loopRunner = queryloop.NewRunner(qe)

	qe.schedulerCoord = enginesched.NewCoordinator(enginesched.CoordinatorConfig{
		Spawner:  qe,
		Logger:   slog.Default(),
		Provider: qe.provider,
		RootDir:  workspaceRootDir(),
	})

	return qe
}

// ApplyMainAgentConfig sets the user-facing main-agent fields on the
// underlying QueryEngineConfig and updates the active prompt profile.
// Used by emma.NewEngine (the L1 wrapper) to install its persona, tool
// palette, and loop cap without exposing internal fields.
//
// Must be called BEFORE the engine processes any message; the L1
// wrapper takes ownership of these fields after construction.
func (qe *QueryEngine) ApplyMainAgentConfig(
	profile *prompt.AgentProfile,
	displayName string,
	allowedTools []string,
	maxTurns int,
) {
	qe.config.MainAgentProfile = profile
	qe.config.MainAgentDisplayName = displayName
	qe.config.MainAgentAllowedTools = allowedTools
	qe.config.MainAgentMaxTurns = maxTurns
	qe.promptProfile = profile
}

// Config returns the active QueryEngineConfig by value. Useful for the
// L1 wrapper and tests that verify configuration was applied.
func (qe *QueryEngine) Config() QueryEngineConfig {
	return qe.config
}

// PromptProfile returns the prompt profile currently driving the main-
// agent loop. Returns the profile installed by ApplyMainAgentConfig, or
// the default chosen at NewQueryEngine time when no L1 wrapper applied.
func (qe *QueryEngine) PromptProfile() *prompt.AgentProfile {
	return qe.promptProfile
}

// Start launches background goroutines that require a long-lived context.
// Must be called once after NewQueryEngine, before the first query.
// ctx should be cancelled when the server shuts down.
func (qe *QueryEngine) Start(ctx context.Context) {
	qe.schedulerCoord.Start(ctx)
	qe.logger.Info("scheduler_coordinator started")
}

// SubmitPlanResponse implements engine.Engine. Routes the user's
// plan.response back to the awaiting requestPlanApproval call via
// sess.Awaits.
func (qe *QueryEngine) SubmitPlanResponse(_ context.Context, sessionID string, resp *types.PlanResponse) error {
	if resp == nil || resp.PlanID == "" {
		return fmt.Errorf("plan response: missing plan_id")
	}
	sess := qe.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session %s not found for plan response", sessionID)
	}
	if err := sess.Awaits.ResolvePlan(resp.PlanID, resp); err != nil {
		return fmt.Errorf("no pending plan request for plan_id %s: %w", resp.PlanID, err)
	}
	return nil
}

// SubmitStepDecision routes a user's step-decision reply back to the
// coordinator that's blocked in requestStepDecision via sess.Awaits.
func (qe *QueryEngine) SubmitStepDecision(_ context.Context, sessionID string, resp *types.StepDecisionResponse) error {
	if resp == nil || resp.RequestID == "" {
		return fmt.Errorf("step decision response: missing request_id")
	}
	sess := qe.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session %s not found for step decision", sessionID)
	}
	if err := sess.Awaits.ResolveStepDecision(resp.RequestID, resp); err != nil {
		return fmt.Errorf("no pending step decision for request_id %s: %w", resp.RequestID, err)
	}
	return nil
}

// newEnvelope builds an emit envelope with the next seq number for the
// given trace. parentEventID, taskID, parentTaskID may be empty when
// not applicable. The caller is responsible for passing the right
// agent_role / agent_id / agent_run_id.
func (qe *QueryEngine) newEnvelope(
	traceID string,
	parentEventID string,
	taskID string,
	parentTaskID string,
	role emit.AgentRole,
	agentID string,
	agentRunID string,
	severity emit.Severity,
) *emit.Envelope {
	return &emit.Envelope{
		EventID:       emit.NewEventID(),
		TraceID:       traceID,
		ParentEventID: parentEventID,
		TaskID:        taskID,
		ParentTaskID:  parentTaskID,
		Seq:           qe.emitSeq.Next(traceID),
		Timestamp:     time.Now().UTC(),
		AgentRole:     role,
		AgentID:       agentID,
		AgentRunID:    agentRunID,
		Severity:      severity,
	}
}

// RegisterPromptSection registers a section with the prompt builder.
// This allows external code to add custom sections.
func (qe *QueryEngine) RegisterPromptSection(section prompt.Section) {
	if qe.promptBuilder != nil {
		// Access the registry through the builder (we'll need to expose it)
		// For now, this is a placeholder - sections should be registered during initialization
		qe.logger.Debug("prompt section registration requested", zap.String("section", section.Name()))
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

	// @-mention routing: detect @agent_name at the start of the user message.
	if qe.mentionParser != nil && qe.defRegistry != nil {
		msgText := extractMessageText(msg)
		if mention := qe.mentionParser.Parse(msgText); mention.AgentName != "" {
			def := qe.defRegistry.Get(mention.AgentName)
			if def != nil {
				// Record the user message before routing to preserve session history.
				sess.AddMessage(*msg)
				return qe.processWithAgent(ctx, sessionID, sess, mention, def)
			}
		}
	}

	sess.AddMessage(*msg)

	// Open a fresh trace for this user request. The trace_id rides on
	// every emit envelope produced during this round so observers can
	// stitch related events back together. Attach it to the request
	// context so deeply-nested tools (scheduler, task)
	// can pull it back out and emit under the same trace.
	traceID := emit.NewTraceID()
	mainAgentRunID := emit.NewAgentRunID()
	startedAt := time.Now()
	userInputSummary := truncateForDisplay(extractMessageText(msg), 240)

	traceCtx := &emit.TraceContext{
		TraceID:   traceID,
		Sequencer: qe.emitSeq,
	}
	ctx = emit.WithTrace(ctx, traceCtx)

	// Create a cancellable context for this query.
	qCtx, cancel := context.WithCancel(ctx)

	// Attach session id + main agent run id so the StatsProvider
	// decorator (and any tracker hook reading from ctx) can attribute
	// LLM usage / tool calls to the right session row. Done before
	// runQueryLoop runs so every downstream call sees the keys.
	qCtx = sessionstats.WithSessionID(qCtx, sess.ID)
	qCtx = sessionstats.WithAgentRunID(qCtx, mainAgentRunID)

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
			// Release the per-trace seq counter so memory does not grow
			// unboundedly on a long-running server.
			qe.emitSeq.Drop(traceID)
		}()

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryStarted,
			Payload: map[string]string{"session_id": sessionID},
		})

		// Emit trace.started before any work begins. Carries a short
		// summary of the user input + a Display block that the client
		// can render as a request card.
		out <- types.EngineEvent{
			Type:     types.EngineEventTraceStarted,
			Text:     userInputSummary,
			Envelope: qe.newEnvelope(traceID, "", "", "", emit.RolePersona, "main", mainAgentRunID, emit.SeverityInfo),
			Display: &emit.Display{
				Title:      "新对话开始",
				Summary:    userInputSummary,
				Visibility: emit.VisibilityCollapsed,
			},
		}

		terminal := qe.runQueryLoop(qCtx, sess, out)

		qe.eventBus.Publish(event.Event{
			Topic:   event.TopicQueryCompleted,
			Payload: map[string]any{"session_id": sessionID, "reason": terminal.Reason, "message": terminal.Message},
		})

		cumUsage := qe.cumulativeUsageFor(sess.ID)
		duration := time.Since(startedAt).Milliseconds()

		// Force-flush the stats persist worker so HTTP fetches after this
		// point see the trace's final numbers without waiting for the next
		// debounce window. Best-effort — failure is logged inside the worker.
		if qe.sessionMgr != nil {
			qe.sessionMgr.FlushStats(qCtx, sess.ID)
		}

		// Choose between trace.finished (success) and trace.failed (any
		// non-completed terminal reason). The body of *.failed carries a
		// developer-facing message; the L1 persona will translate it
		// into user-facing prose itself, so we don't try to localize here.
		traceEventType := types.EngineEventTraceFinished
		traceTitle := "对话已完成"
		traceSeverity := emit.SeverityInfo
		var traceErr error
		switch terminal.Reason {
		case types.TerminalCompleted:
			// success — keep defaults
		case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
			traceEventType = types.EngineEventTraceFailed
			traceTitle = "对话已中断"
			traceSeverity = emit.SeverityWarn
			traceErr = fmt.Errorf("aborted: %s", terminal.Reason)
		default:
			traceEventType = types.EngineEventTraceFailed
			traceTitle = "对话失败"
			traceSeverity = emit.SeverityError
			if terminal.Message != "" {
				traceErr = fmt.Errorf("%s: %s", terminal.Reason, terminal.Message)
			} else {
				traceErr = fmt.Errorf("%s", terminal.Reason)
			}
		}

		out <- types.EngineEvent{
			Type:     traceEventType,
			Terminal: &terminal,
			Error:    traceErr,
			Envelope: qe.newEnvelope(traceID, "", "", "", emit.RolePersona, "main", mainAgentRunID, traceSeverity),
			Display: &emit.Display{
				Title:      traceTitle,
				Visibility: emit.VisibilityCollapsed,
			},
			Metrics: &emit.Metrics{
				DurationMs: duration,
				TokensIn:   cumUsage.InputTokens,
				TokensOut:  cumUsage.OutputTokens,
				CacheRead:  cumUsage.CacheRead,
				CacheWrite: cumUsage.CacheWrite,
			},
		}

		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: &terminal,
			Usage:    &cumUsage,
		}
	}()

	return out, nil
}

// SubmitToolResult implements Engine. It delivers a client-side tool result
// to the waiting query loop goroutine via sess.Awaits.
func (qe *QueryEngine) SubmitToolResult(_ context.Context, sessionID string, result *types.ToolResultPayload) error {
	sess := qe.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session lookup for tool result: session %s not found", sessionID)
	}
	if err := sess.Awaits.ResolveTool(result); err != nil {
		return fmt.Errorf("no pending tool call for tool_use_id %s: %w", result.ToolUseID, err)
	}
	return nil
}

// SubmitPermissionResult implements Engine. It delivers a permission approval/denial
// from the client to the waiting tool executor via sess.Awaits.
func (qe *QueryEngine) SubmitPermissionResult(_ context.Context, sessionID string, resp *types.PermissionResponse) error {
	sess := qe.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session lookup for permission result: session %s not found", sessionID)
	}
	if err := sess.Awaits.ResolvePerm(resp.RequestID, resp); err != nil {
		return fmt.Errorf("no pending permission request for request_id %s: %w", resp.RequestID, err)
	}
	return nil
}

// AbortSession implements Engine. Cancels the in-flight query for a session.
func (qe *QueryEngine) AbortSession(_ context.Context, sessionID string) error {
	qe.mu.Lock()
	cancel, ok := qe.cancels[sessionID]
	if ok {
		delete(qe.cancels, sessionID)
	}
	qe.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active query for session %s", sessionID)
	}
	cancel()

	// GC pending awaits so any waiters blocked on result channels
	// unblock immediately (closed-channel signal) instead of only via
	// the slower ctx.Done() path. Best-effort: session may have been
	// GC'd between cancel and lookup.
	if sess := qe.sessionMgr.Get(sessionID); sess != nil && sess.Awaits != nil {
		sess.Awaits.AbortAll("aborted")
	}
	return nil
}

// requestPlanApproval delegates to queryloop.Runner.RequestPlanApproval.
func (qe *QueryEngine) requestPlanApproval(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	proposal *types.PlanProposal,
) (*types.PlanResponse, error) {
	return qe.loopRunner.RequestPlanApproval(ctx, sessionID, out, proposal)
}

// requestStepDecision delegates to queryloop.Runner.RequestStepDecision.
func (qe *QueryEngine) requestStepDecision(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	req *types.StepDecisionRequest,
) (*types.StepDecisionResponse, error) {
	return qe.loopRunner.RequestStepDecision(ctx, sessionID, out, req)
}
