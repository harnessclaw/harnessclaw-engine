// Package emma owns the user-facing query engine — reachable from the
// WebSocket / API channel. Engine collapses the former
// engine.QueryEngine + queryloop.Runner + emma.L1Engine three-layer proxy
// into one type that drives the 5-phase query loop directly.
package emma

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel/emit"
	"harnessclaw-go/internal/commands"
	schedulerpkg "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/emma/mention"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/sections"
	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/legacy/workspace"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skills"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// Named ctx-cancel causes. Downstream (provider / bifrost / llmcall) calls
// context.Cause(ctx) to distinguish these from anonymous cancellations.
var (
	// errEmmaSessionEnded is set when the ProcessMessage goroutine exits
	// normally (query completed or terminal reached) and the deferred
	// cancel() runs. Treat as "no upstream cancel happened".
	errEmmaSessionEnded = errors.New("emma: ProcessMessage goroutine exited (normal)")

	// errEmmaAborted is set by AbortSession — i.e. the user pressed stop /
	// the client called the abort endpoint / a wire frame requested it.
	errEmmaAborted = errors.New("emma: AbortSession invoked by caller")
)

// Engine is emma's concrete implementation that runs the 5-phase query loop.
//
// It folds together what used to be three layers:
//   - engine.QueryEngine (assembler + 30 getters + forwarding methods)
//   - queryloop.Runner   (real orchestrator)
//   - emma.L1Engine      (thin proxy applying emma persona)
//
// Engine owns all dependencies as fields and dispatches sub-agent
// spawns through spawner (internal/engine/spawn), which routes by
// SubagentType to tier modules in internal/engine/agent/*.
type Engine struct {
	provider    provider.Provider
	registry    *tool.Registry
	cmdRegistry *command.Registry
	sessionMgr  *session.Manager
	compactor   compact.Compactor
	permChecker permission.Checker
	logger      *zap.Logger
	config      Config

	// retryer drives the per-LLM-call retry loop with exponential
	// backoff + jitter + 529-fallback signalling. Shared across every
	// LLM call (main loop + sub-agents) so the consecutive-529 counter
	// accumulates session-wide.
	retryer *retry.Retryer

	// Prompt builder for structured system prompt assembly.
	promptBuilder *prompt.Builder
	promptProfile *prompt.AgentProfile

	// In-flight session tracking for abort support.
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	// causeCancels parallels `cancels` but carries the explicit cancel-cause
	// function so AbortSession (and tracing) can attach a named cause to
	// the context. Only populated for ProcessMessage-owned contexts; the
	// public RegisterCancel API still accepts a plain context.CancelFunc
	// for callers that don't need cause attribution.
	causeCancels map[string]context.CancelCauseFunc

	// Multi-agent support.
	agentRegistry *agent.AgentRegistry
	messageBroker *agent.MessageBroker
	defRegistry   *agent.AgentDefinitionRegistry
	mentionRouter *mention.Router

	// skillReader provides runtime skill discovery for search_skill /
	// load_skill tools (used by freelancer L3). nil disables them.
	skillReader *skill.Reader

	// emitSeq dispenses per-trace sequence numbers for the emit envelope.
	emitSeq *emit.Sequencer

	// statsRegistry, when non-nil, lets the engine attribute LLM /
	// sub-agent / tool activity to the correct Tracker.
	statsRegistry *sessionstats.Registry

	// sched 是新的 scheduler.Scheduler dispatch 入口。
	// 所有 callsite (tools / mention router / future coordinator) 走这一个。
	// emma.Scheduler() 公开访问。
	sched schedulerpkg.Scheduler

	// skillListing is the lazy-computed once-per-engine catalog string
	// passed into the SkillsSection prompt block.
	skillListingOnce sync.Once
	skillListing     string
}

// Option configures Engine at construction.
type Option func(*Engine)

// EmmaConfig carries the emma-persona overlay applied via WithEmmaConfig. All
// fields have sensible defaults; an empty EmmaConfig is valid and produces
// the canonical emma setup.
//
// emma tool palette rationale (post 3-tier refactor):
//   - scheduler                   → THE delegation entry point. emma never
//     picks between single-step / multi-step
//     or specific sub-agents — the scheduler
//     (L2) handles all decomposition.
//   - web_search / tavily_search  → emma's own *light* fact-finding for
//     context gathering before dispatching.
//   - ask_user_question           → clarification when the request is
//     ambiguous.
//
// The task tool is intentionally NOT in this list — it lives inside the
// L2 layer (the scheduler uses task internally to dispatch L3).
type EmmaConfig struct {
	// Profile is the prompt profile used for the emma main agent.
	// Default: prompt.EmmaProfile.
	Profile *prompt.AgentProfile

	// DisplayName is the friendly leader name interpolated into worker
	// identity prompts. Default: "emma".
	DisplayName string

	// AllowedTools restricts the tools advertised to the emma LLM.
	// Default: scheduler + light context tools.
	AllowedTools []string

	// MaxTurns caps the emma loop. Default: 10.
	MaxTurns int
}

// DefaultEmmaConfig returns the canonical emma configuration.
//
// emma is the persona/dispatch surface; file inspection (read/glob/grep)
// is intentionally NOT in the palette so emma cannot "take over" a task
// halfway and burn the small-loop turn budget on local file checks. If
// inspection is needed, emma dispatches scheduler — scheduler/L3 own
// every filesystem read and report findings back.
func DefaultEmmaConfig() EmmaConfig {
	return EmmaConfig{
		Profile:     prompt.EmmaProfile,
		DisplayName: "emma",
		AllowedTools: []string{
			"dispatch",
			"web_search",
			"tavily_search",
			"ask_user_question",
		},
		MaxTurns: 15,
	}
}

// WithEmmaConfig applies an EmmaConfig overlay — sets the main agent profile,
// display name, tool palette, and small-loop cap. Empty fields fall back
// to DefaultEmmaConfig defaults.
func WithEmmaConfig(cfg EmmaConfig) Option {
	if cfg.Profile == nil {
		cfg.Profile = prompt.EmmaProfile
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "emma"
	}
	if len(cfg.AllowedTools) == 0 {
		cfg.AllowedTools = []string{
			"dispatch",
			"web_search",
			"tavily_search",
			"ask_user_question",
		}
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 15
	}
	return func(e *Engine) {
		e.config.MainAgentProfile = cfg.Profile
		e.config.MainAgentDisplayName = cfg.DisplayName
		e.config.MainAgentAllowedTools = cfg.AllowedTools
		e.config.MainAgentMaxTurns = cfg.MaxTurns
		e.promptProfile = cfg.Profile
	}
}

// New constructs the emma engine. opts apply after the core wiring is up so
// EmmaConfig (or any future overlay) can mutate the resolved main-agent
// fields before the first ProcessMessage.
func New(
	prov provider.Provider,
	reg *tool.Registry,
	mgr *session.Manager,
	comp compact.Compactor,
	perm permission.Checker,
	logger *zap.Logger,
	cfg Config,
	cmdReg *command.Registry,
	opts ...Option,
) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}

	promptRegistry := prompt.NewRegistry()
	promptRegistry.Register(sections.NewCurrentDateSection())
	promptRegistry.Register(sections.NewRoleSection())
	promptRegistry.Register(sections.NewTeamSection())
	promptRegistry.Register(sections.NewPrinciplesSection())
	promptRegistry.Register(sections.NewToolsSection())
	promptRegistry.Register(sections.NewEnvSection())
	promptRegistry.Register(sections.NewMemorySection())
	promptRegistry.Register(sections.NewSkillsSection())
	promptRegistry.Register(sections.NewTaskSection())
	promptBuilder := prompt.NewBuilder(promptRegistry, logger)

	promptProfile := cfg.MainAgentProfile
	if promptProfile == nil {
		promptProfile = prompt.WorkerProfile
	}

	e := &Engine{
		provider:      prov,
		registry:      reg,
		cmdRegistry:   cmdReg,
		sessionMgr:    mgr,
		compactor:     comp,
		permChecker:   perm,
		logger:        logger,
		config:        cfg,
		promptBuilder: promptBuilder,
		promptProfile: promptProfile,
		cancels:       make(map[string]context.CancelFunc),
		causeCancels:  make(map[string]context.CancelCauseFunc),
		emitSeq:       emit.NewSequencer(),
		retryer:       retry.New(retryConfigFromCfg(cfg), logger),
	}

	// Optional config-injected dependencies. Engine treats nil as
	// "feature disabled".
	if cfg.DefRegistry != nil {
		e.defRegistry = cfg.DefRegistry
	}
	if cfg.SkillReader != nil {
		e.skillReader = cfg.SkillReader
	}
	if cfg.StatsRegistry != nil {
		e.statsRegistry = cfg.StatsRegistry
	}

	// 老的 spawn.Spawner + builtin module 注册已删除：
	// 新 scheduler.Runtime.LLM 用 SpawnParams.Definition 驱动 sub-agent loop，
	// 不再走 SubagentType → spawn.Module 路由。每种 sub-agent 的差异（freelancer
	// 的 skill 注入 / explore 的 OnPoolBuilt 等）后续按需在 Runtime 内 inline 或
	// 通过 Definition 数据描述（如 LoadedSkills / OnPoolBuiltHook 字段）。
	_ = prov
	_ = mgr
	_ = comp
	_ = promptBuilder
	_ = reg

	// 新 scheduler dispatch 入口。callsite 已全部迁移；
	// 旧的 schedulerCoord / agentRun / agentscheduler.Module 已删（这次清理）。
	e.sched = wireScheduler(wiredDeps{
		Provider:      e.provider,
		ToolRegistry:  reg,
		SessionMgr:    mgr,
		Compactor:     comp,
		Retryer:       e.retryer,
		PromptBuilder: promptBuilder,
		SkillReader:   e.skillReader, // 透传 emma cfg 的 SkillReader 给 freelancer skill hydration
		Logger:        logger,
		Cfg:           cfg,
		WorkspaceRoot: workspace.DefaultRootDir(),
	})

	// @-mention router — only wired when a definition registry is
	// provided. Holds the spawner so emma's ProcessMessage can offload
	// agent dispatch without recomputing routing or owning a parser.
	if e.defRegistry != nil {
		// mention router 走新 scheduler.Scheduler 接口。
		// agentRun 字段保留是给其他还未迁移的 tools 用。
		e.mentionRouter = mention.NewRouter(
			e.sched,
			e.defRegistry,
			agent.NewMentionParser(e.defRegistry),
		)
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// Scheduler 暴露 emma 装配的新 scheduler dispatch 入口。
// 过渡阶段的访问器：tools / mention router / future coordinator 走这一个。
func (e *Engine) Scheduler() schedulerpkg.Scheduler { return e.sched }

// Config returns the active engine configuration by value.
func (e *Engine) Config() Config { return e.config }

// PromptProfile returns the profile currently driving the main-agent loop.
func (e *Engine) PromptProfile() *prompt.AgentProfile { return e.promptProfile }

// （删）原 Engine.Spawner() 暴露 *spawn.Spawner —— 新架构里调用方走
// Engine.Scheduler() 拿 scheduler.Scheduler 接口分发。

// SetAgentRegistry configures the agent registry for async agent support.
func (e *Engine) SetAgentRegistry(reg *agent.AgentRegistry) { e.agentRegistry = reg }

// SetMessageBroker configures the message broker for inter-agent communication.
func (e *Engine) SetMessageBroker(broker *agent.MessageBroker) { e.messageBroker = broker }

// Start launches background goroutines that require a long-lived context.
// Must be called once after New, before the first query. ctx should be
// cancelled when the server shuts down.
//
// 新 scheduler 是 stateless dispatch 入口（构造完即可用），无后台 goroutine
// 需启动。保留 Start 作为 lifecycle hook，未来 coordinator 模块可挂在这里。
func (e *Engine) Start(_ context.Context) {
	e.logger.Info("emma engine started")
}

// ProcessMessage implements engine.Engine. It appends the user message to
// the session, applies @-mention routing if enabled, and runs the query
// loop, emitting events on the returned channel.
//
// Workspace bootstrap (plan.json + tasks/ + deliverables/) is deferred
// to the L2 scheduler module entry point (see
// internal/engine/agent/scheduler/module.go). emma itself never writes
// to the workspace, so an unconditional bootstrap here was littering
// disk with empty session directories for trivial queries like "weather
// in hefei" that never dispatch a sub-agent.
func (e *Engine) ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error) {
	sess, err := e.sessionMgr.GetOrCreate(ctx, sessionID, "", "")
	if err != nil {
		return nil, fmt.Errorf("session get-or-create: %w", err)
	}

	// @-mention routing: detect @agent_name at the start of the user
	// message and offload to mention.Router. Router appends msg to
	// sess itself before spawning, so we MUST NOT add it here when
	// TryRoute returns a non-nil channel.
	if e.mentionRouter != nil {
		if out := e.mentionRouter.TryRoute(ctx, sess, msg); out != nil {
			return out, nil
		}
	}

	sess.AddMessage(*msg)

	// Open a fresh trace for this user request.
	traceID := emit.NewTraceID()
	mainAgentRunID := emit.NewAgentRunID()
	startedAt := time.Now()
	userInputSummary := TruncateForDisplay(ExtractMessageText(msg), 240)

	traceCtx := &emit.TraceContext{
		TraceID:   traceID,
		Sequencer: e.emitSeq,
	}
	ctx = emit.WithTrace(ctx, traceCtx)

	// WithCancelCause lets every cancellation carry a typed reason
	// (errEmmaSessionEnded / errEmmaAborted / parent ctx cause). Downstream
	// providers can call context.Cause(ctx) to surface "session goroutine
	// exited" vs "user pressed stop" vs "ws closed" instead of always
	// seeing "context canceled".
	qCtx, causeCancel := context.WithCancelCause(ctx)
	cancel := func() { causeCancel(errEmmaSessionEnded) }

	qCtx = sessionstats.WithSessionID(qCtx, sess.ID)
	qCtx = sessionstats.WithAgentRunID(qCtx, mainAgentRunID)
	// emma IS the root for her own session —— sub-agent 派生时通过 ctx 拿这俩，
	// 不注的话下游 dispatch tool 读不到 RootSessionID，会导致 freelancer 的
	// cfg.RootSessionID 为空，WorkspacePrelude 不输出"工作上下文"段，
	// meta_write/submit_task_result 报 "SessionRoot/TaskID missing" 一连串错。
	qCtx = sessionstats.WithRootSessionID(qCtx, sess.ID)
	qCtx = sessionstats.WithImmediateParentSessionID(qCtx, sess.ID)

	e.mu.Lock()
	e.cancels[sessionID] = cancel
	e.causeCancels[sessionID] = causeCancel
	e.mu.Unlock()

	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)
		defer func() {
			e.mu.Lock()
			delete(e.cancels, sessionID)
			delete(e.causeCancels, sessionID)
			e.mu.Unlock()
			cancel()
			e.emitSeq.Drop(traceID)
		}()

		e.logger.Debug("query started", zap.String("session_id", sessionID))

		out <- types.EngineEvent{
			Type:     types.EngineEventTraceStarted,
			Text:     userInputSummary,
			Envelope: e.newEnvelope(traceID, "", "", "", emit.RolePersona, "main", mainAgentRunID, emit.SeverityInfo),
			Display: &emit.Display{
				Title:      "新对话开始",
				Summary:    userInputSummary,
				Visibility: emit.VisibilityCollapsed,
			},
		}

		approvalFn := func(ctx context.Context, evtOut chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
			return e.requestPermissionApproval(ctx, evtOut, sess.ID, req)
		}
		terminal := e.run(qCtx, sess, out, approvalFn)

		e.logger.Info("query completed",
			zap.String("session_id", sessionID),
			zap.String("reason", string(terminal.Reason)),
			zap.String("message", terminal.Message),
		)

		cumUsage := e.cumulativeUsageFor(sess.ID)
		duration := time.Since(startedAt).Milliseconds()

		if e.sessionMgr != nil {
			e.sessionMgr.FlushStats(qCtx, sess.ID)
		}

		traceEventType := types.EngineEventTraceFinished
		traceTitle := "对话已完成"
		traceSeverity := emit.SeverityInfo
		var traceErr error
		switch terminal.Reason {
		case types.TerminalCompleted:
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
			Envelope: e.newEnvelope(traceID, "", "", "", emit.RolePersona, "main", mainAgentRunID, traceSeverity),
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

// RegisterCancel stores the per-session cancel func so AbortSession can
// reach it. Used by tests that simulate an in-flight query without going
// through ProcessMessage.
func (e *Engine) RegisterCancel(sid string, cancel context.CancelFunc) {
	e.mu.Lock()
	e.cancels[sid] = cancel
	e.mu.Unlock()
}

// DeregisterCancel removes the per-session cancel func registered via
// RegisterCancel.
func (e *Engine) DeregisterCancel(sid string) {
	e.mu.Lock()
	delete(e.cancels, sid)
	e.mu.Unlock()
}

// AbortSession implements engine.Engine. Cancels the in-flight query for
// a session.
func (e *Engine) AbortSession(_ context.Context, sessionID string) error {
	e.mu.Lock()
	cancel, ok := e.cancels[sessionID]
	causeCancel, hasCause := e.causeCancels[sessionID]
	if ok {
		delete(e.cancels, sessionID)
		delete(e.causeCancels, sessionID)
	}
	e.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active query for session %s", sessionID)
	}
	// Prefer the cause-aware cancel so downstream `context.Cause(ctx)` reads
	// `errEmmaAborted` instead of the generic `context.Canceled`.
	if hasCause {
		causeCancel(errEmmaAborted)
	} else {
		cancel()
	}
	e.logger.Info("AbortSession invoked",
		zap.String("session_id", sessionID),
		zap.Bool("cause_aware", hasCause),
	)

	if sess := e.sessionMgr.Get(sessionID); sess != nil && sess.Awaits != nil {
		sess.Awaits.AbortAll("aborted")
	}
	return nil
}

// newEnvelope builds an emit envelope with the next seq number for the
// given trace. parentEventID, taskID, parentTaskID may be empty when not
// applicable.
func (e *Engine) newEnvelope(
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
		Seq:           e.emitSeq.Next(traceID),
		Timestamp:     time.Now().UTC(),
		AgentRole:     role,
		AgentID:       agentID,
		AgentRunID:    agentRunID,
		Severity:      severity,
	}
}

// contextWindow returns the effective auto-compact budget — the operator
// value when >0, else a 200_000 token fallback aligned with modern
// provider defaults (claude / gpt-5 / gemini all sit at 200k).
func (e *Engine) contextWindow() int {
	return effectiveContextWindow(e.config.ContextWindow)
}

// plannerModel returns the LLM model the LLMPlanner should call.
// Provider may expose a default model via Name() or — until we wire a
// dedicated config field — we let the provider pick (empty string).
func (e *Engine) plannerModel() string { return "" }

// session is imported above for the *session.Manager field; this no-op
// keeps goimports from dropping it when other symbols in the file
// don't reference the package directly.
var _ = session.StateActive
