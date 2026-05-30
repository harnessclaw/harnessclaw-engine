// Package emma owns the user-facing query engine — reachable from the
// WebSocket / API channel. Engine collapses the former
// engine.QueryEngine + queryloop.Runner + emma.L1Engine three-layer proxy
// into one type that drives the 5-phase query loop directly.
package emma

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
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// Engine is emma's concrete implementation that runs the 5-phase query loop.
//
// It folds together what used to be three layers:
//   - engine.QueryEngine (assembler + 30 getters + forwarding methods)
//   - queryloop.Runner   (real orchestrator)
//   - emma.L1Engine      (thin proxy applying emma persona)
//
// Engine owns all dependencies as fields and exposes spawn.Deps methods on
// itself so the spawn package can drive sub-agent runs without going
// through a separate facade.
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

	// Multi-agent support.
	agentRegistry *agent.AgentRegistry
	messageBroker *agent.MessageBroker
	defRegistry   *agent.AgentDefinitionRegistry
	mentionParser *MentionParser

	// skillReader provides runtime skill discovery for search_skill /
	// load_skill tools (used by freelancer L3). nil disables them.
	skillReader *skill.Reader

	// spawner owns the sub-agent lifecycle: taskRegistry,
	// searchGapDetector, and the 14-step SpawnSync pipeline.
	spawner *spawn.Spawner

	// emitSeq dispenses per-trace sequence numbers for the emit envelope.
	emitSeq *emit.Sequencer

	// statsRegistry, when non-nil, lets the engine attribute LLM /
	// sub-agent / tool activity to the correct Tracker.
	statsRegistry *sessionstats.Registry

	// schedulerCoord is the L2 scheduler.Coordinator instance.
	schedulerCoord *enginesched.Coordinator

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
//                                   picks between single-step / multi-step
//                                   or specific sub-agents — the scheduler
//                                   (L2) handles all decomposition.
//   - web_search / tavily_search  → emma's own *light* fact-finding for
//                                   context gathering before dispatching.
//   - ask_user_question           → clarification when the request is
//                                   ambiguous.
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
func DefaultEmmaConfig() EmmaConfig {
	return EmmaConfig{
		Profile:     prompt.EmmaProfile,
		DisplayName: "emma",
		AllowedTools: []string{
			"scheduler",
			"web_search",
			"tavily_search",
			"ask_user_question",
			"read",
			"glob",
			"grep",
		},
		MaxTurns: 10,
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
			"scheduler",
			"web_search",
			"tavily_search",
			"ask_user_question",
			"read",
			"glob",
			"grep",
		}
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 10
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
		emitSeq:       emit.NewSequencer(),
		retryer:       retry.New(retryConfigFromCfg(cfg), logger),
	}

	// Optional config-injected dependencies. Engine treats nil as
	// "feature disabled".
	if cfg.DefRegistry != nil {
		e.defRegistry = cfg.DefRegistry
		e.mentionParser = NewMentionParser(cfg.DefRegistry)
	}
	if cfg.SkillReader != nil {
		e.skillReader = cfg.SkillReader
	}
	if cfg.StatsRegistry != nil {
		e.statsRegistry = cfg.StatsRegistry
	}

	// Spawner depends on Engine via spawn.Deps. Constructed after the
	// optional deps land so spawn sees the fully wired state.
	e.spawner = spawn.NewSpawner(e)

	e.schedulerCoord = enginesched.NewCoordinator(enginesched.CoordinatorConfig{
		Spawner:  e,
		Logger:   slog.Default(),
		Provider: e.provider,
		RootDir:  workspace.DefaultRootDir(),
	})

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// Config returns the active engine configuration by value.
func (e *Engine) Config() Config { return e.config }

// PromptProfile returns the profile currently driving the main-agent loop.
func (e *Engine) PromptProfile() *prompt.AgentProfile { return e.promptProfile }

// SetAgentRegistry configures the agent registry for async agent support.
func (e *Engine) SetAgentRegistry(reg *agent.AgentRegistry) { e.agentRegistry = reg }

// SetMessageBroker configures the message broker for inter-agent communication.
func (e *Engine) SetMessageBroker(broker *agent.MessageBroker) { e.messageBroker = broker }

// Start launches background goroutines that require a long-lived context.
// Must be called once after New, before the first query. ctx should be
// cancelled when the server shuts down.
func (e *Engine) Start(ctx context.Context) {
	e.schedulerCoord.Start(ctx)
	e.logger.Info("emma engine scheduler coordinator started")
}

// ProcessMessage implements engine.Engine. It appends the user message to
// the session, applies @-mention routing if enabled, and runs the query
// loop, emitting events on the returned channel.
//
// Side effect: idempotently bootstraps the on-disk workspace for the
// session ({rootDir}/session/{sessionID}/{plan.json,tasks/,deliverables/})
// so plan_update / meta_write / promote find the layout already in place.
func (e *Engine) ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error) {
	if root := workspace.DefaultRootDir(); root != "" {
		if err := workspace.EnsureSession(root, sessionID); err != nil {
			e.logger.Warn("ensure session workspace",
				zap.String("session_id", sessionID),
				zap.Error(err),
			)
		}
	}

	sess, err := e.sessionMgr.GetOrCreate(ctx, sessionID, "", "")
	if err != nil {
		return nil, fmt.Errorf("session get-or-create: %w", err)
	}

	// @-mention routing: detect @agent_name at the start of the user message.
	if e.mentionParser != nil && e.defRegistry != nil {
		msgText := ExtractMessageText(msg)
		if mention := e.mentionParser.Parse(msgText); mention.AgentName != "" {
			def := e.defRegistry.Get(mention.AgentName)
			if def != nil {
				sess.AddMessage(*msg)
				return e.ProcessWithAgent(ctx, sessionID, sess, mention, def)
			}
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

	qCtx, cancel := context.WithCancel(ctx)

	qCtx = sessionstats.WithSessionID(qCtx, sess.ID)
	qCtx = sessionstats.WithAgentRunID(qCtx, mainAgentRunID)

	e.mu.Lock()
	e.cancels[sessionID] = cancel
	e.mu.Unlock()

	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)
		defer func() {
			e.mu.Lock()
			delete(e.cancels, sessionID)
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
	if ok {
		delete(e.cancels, sessionID)
	}
	e.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active query for session %s", sessionID)
	}
	cancel()

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

// session is imported above to satisfy the spawn.Deps signature; this
// no-op keeps `goimports` from dropping it when other symbols in the
// file don't reference the package directly.
var _ = session.StateActive
