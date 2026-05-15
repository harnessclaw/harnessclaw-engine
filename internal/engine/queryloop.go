package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/sections"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/pkg/types"
)

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
	// L1Engine sets this to ["Agent","Orchestrate"] so emma can only delegate.
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
	provider     provider.Provider
	registry     *tool.Registry
	cmdRegistry  *command.Registry
	sessionMgr   *session.Manager
	compactor    compact.Compactor
	permChecker  permission.Checker
	eventBus     *event.Bus
	logger       *zap.Logger
	config       QueryEngineConfig
	artifactStore any // *artifact.Store; kept untyped here to avoid the import cycle

	// retryer drives the per-LLM-call retry loop with exponential
	// backoff + jitter + 529-fallback signalling. Shared across every
	// LLM call (main loop + sub-agents) so the consecutive-529
	// counter accumulates session-wide; that's intentional —
	// connections to the same gateway share the same upstream health.
	retryer *retry.Retryer

	// Prompt builder for structured system prompt assembly.
	promptBuilder *prompt.Builder
	promptProfile *prompt.AgentProfile

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

	// Per-session prompt cache. Caches the entire built system prompt to avoid
	// rebuilding on every turn when inputs haven't changed.
	promptCacheMu sync.RWMutex
	promptCache   map[string]*promptCacheEntry // session_id → cache entry

	// Multi-agent support fields.
	agentRegistry  *agent.AgentRegistry
	messageBroker  *agent.MessageBroker
	defRegistry    *agent.AgentDefinitionRegistry
	mentionParser  *MentionParser

	// TaskRegistry stores completed sub-agent results by agentID.
	// Full output is kept here; emma only receives summaries via tool_result.
	// Used for context passing (depends_on) and debugging.
	// TODO(phase2): add TTL or LRU eviction to prevent unbounded growth on long-running servers.
	taskRegistryMu sync.RWMutex
	taskRegistry   map[string]*agent.SpawnResult // agentID → full result

	// emitSeq dispenses per-trace sequence numbers for the emit envelope.
	// Backed by sync.Map internally so concurrent traces don't contend on a
	// global mutex.
	emitSeq *emit.Sequencer

	// coordinators routes coordinator-tier (L2) spawns through a
	// Coordinator implementation chosen by CoordinatorMode. Built-in
	// modes (react / plan) are registered in NewQueryEngine; tests can
	// register additional modes post-construction.
	coordinators *coordinatorRegistry

	// pendingPlans tracks in-flight PlanCoordinator approval requests.
	// Indexed by plan_id (server-generated). Each entry holds a result
	// channel the coordinator is blocking on; SubmitPlanResponse looks
	// up by plan_id and pushes the response. Same pattern as
	// pendingPerms / pendingTools.
	planMu       sync.Mutex
	pendingPlans map[string]*pendingPlanReq

	// pendingStepDecisions tracks in-flight step-decision requests
	// (continue / retry / cancel after a step or plan-level failure).
	// Indexed by request_id (server-generated). Mirrors pendingPlans.
	stepDecisionMu       sync.Mutex
	pendingStepDecisions map[string]*pendingStepDecisionReq

	// statsRegistry, when non-nil, lets the engine read cumulative stats
	// (see cumulativeUsageFor) and lets sub-agent hooks/tool hooks ping
	// the right Tracker. Set via SetStatsRegistry from cmd/server/main.go;
	// tests that don't enable metrics leave it nil.
	statsRegistry *sessionstats.Registry

	// sessionManager, when non-nil, lets the engine reach Manager.FlushStats
	// at trace lifecycle boundaries. Set via SetSessionManager from
	// cmd/server/main.go.
	sessionManager *session.Manager
}

// pendingPlanReq tracks one in-flight plan approval request. The
// PlanCoordinator pushes a request and blocks on response; the channel
// adapter routes the user's plan.response message into SubmitPlanResponse,
// which fills response and closes the channel.
type pendingPlanReq struct {
	planID    string
	sessionID string
	response  chan *types.PlanResponse
	createdAt time.Time
}

// pendingStepDecisionReq is the step-decision counterpart of
// pendingPlanReq.
type pendingStepDecisionReq struct {
	requestID string
	sessionID string
	response  chan *types.StepDecisionResponse
	createdAt time.Time
}

// promptCacheEntry stores a cached system prompt and the conditions under which it was built.
// The cache is invalidated when any input changes enough to affect the output.
type promptCacheEntry struct {
	prompt       string              // cached ToSystemPrompt() result
	output       *prompt.PromptOutput // full output (for observability)
	budget       int                 // budget when cached
	hasTask      bool                // whether task state was present
	memoryLen    int                 // len(memory) when cached
	date         string              // date when cached (YYYY-MM-DD)
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
	promptRegistry.Register(sections.NewArtifactsSection())
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

	coordReg := newCoordinatorRegistry()
	registerBuiltinCoordinators(coordReg)

	return &QueryEngine{
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
		pendingTools:      make(map[string]*pendingToolCall),
		pendingPerms:      make(map[string]*pendingPermission),
		sessionAllowTools: make(map[string]map[string]bool),
		promptCache:       make(map[string]*promptCacheEntry),
		taskRegistry:      make(map[string]*agent.SpawnResult),
		emitSeq:           emit.NewSequencer(),
		coordinators:      coordReg,
		pendingPlans:         make(map[string]*pendingPlanReq),
		pendingStepDecisions: make(map[string]*pendingStepDecisionReq),
		retryer:              retry.New(retryConfigFromEngineCfg(cfg), logger),
	}
}

// requestPlanApproval registers a pending plan request, emits the
// proposal event so the channel can forward it, and blocks until the
// client sends back a plan.response (or ctx is cancelled).
//
// Returns:
//   - resp: client's response (Approved + optional UpdatedSteps)
//   - err:  ctx cancellation, or "client never responded" timeout
//
// Used by PlanCoordinator. The function is non-method-style
// (`func (qe *QueryEngine)`) so future implementations of the
// approval mechanism (HTTP callback, kafka, etc.) can swap in via
// dependency injection without changing the call site.
func (qe *QueryEngine) requestPlanApproval(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	proposal *types.PlanProposal,
) (*types.PlanResponse, error) {
	if proposal == nil {
		return nil, fmt.Errorf("plan approval: nil proposal")
	}
	if proposal.PlanID == "" {
		return nil, fmt.Errorf("plan approval: empty plan_id")
	}

	req := &pendingPlanReq{
		planID:    proposal.PlanID,
		sessionID: sessionID,
		response:  make(chan *types.PlanResponse, 1),
		createdAt: time.Now(),
	}

	qe.planMu.Lock()
	qe.pendingPlans[proposal.PlanID] = req
	qe.planMu.Unlock()

	// Best-effort cleanup on exit. SubmitPlanResponse also deletes
	// after delivery; double-delete is safe (map is keyed and
	// idempotent on missing keys).
	defer func() {
		qe.planMu.Lock()
		delete(qe.pendingPlans, proposal.PlanID)
		qe.planMu.Unlock()
	}()

	// Emit the proposal event. The router/channel forwards to the
	// client. If the channel doesn't exist (tests), we still register
	// the pending request and let the caller drive SubmitPlanResponse
	// directly.
	if out != nil {
		select {
		case out <- types.EngineEvent{
			Type:         types.EngineEventPlanProposed,
			AgentID:      proposal.AgentID,
			PlanProposal: proposal,
		}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Block on response or context cancellation.
	select {
	case resp := <-req.response:
		// Echo "approved" event so the client can match its own
		// confirmation cycle and the trace shows the round-trip.
		if out != nil && resp != nil {
			out <- types.EngineEvent{
				Type:         types.EngineEventPlanApproved,
				AgentID:      proposal.AgentID,
				PlanProposal: proposal,
			}
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitPlanResponse implements engine.Engine. Routes the user's
// plan.response back to the awaiting requestPlanApproval call. Returns
// nil on success; logs at warn level when the plan_id has no pending
// request (stale / duplicate response).
func (qe *QueryEngine) SubmitPlanResponse(_ context.Context, _ string, resp *types.PlanResponse) error {
	if resp == nil || resp.PlanID == "" {
		return fmt.Errorf("plan response: missing plan_id")
	}
	qe.planMu.Lock()
	req, ok := qe.pendingPlans[resp.PlanID]
	if ok {
		delete(qe.pendingPlans, resp.PlanID)
	}
	qe.planMu.Unlock()
	if !ok {
		qe.logger.Warn("plan response for unknown plan_id (stale or duplicate)",
			zap.String("plan_id", resp.PlanID),
		)
		return nil // not a hard error; client may retry
	}
	// Non-blocking send — the channel is buffered (cap=1) and we
	// already own the slot.
	req.response <- resp
	return nil
}

// requestStepDecision pauses the coordinator on a hard step / plan
// failure and asks the user how to proceed. Mirrors requestPlanApproval:
// emits a step_decision_requested event, registers the in-flight req
// keyed by request_id, blocks on either user response or ctx
// cancellation. Caller is expected to strip the inherited tool-ctx
// deadline (context.WithoutCancel) so the user gets unbounded time —
// same policy as plan_review and AskUserQuestion.
func (qe *QueryEngine) requestStepDecision(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	req *types.StepDecisionRequest,
) (*types.StepDecisionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("step decision: nil request")
	}
	if req.RequestID == "" {
		return nil, fmt.Errorf("step decision: empty request_id")
	}

	pending := &pendingStepDecisionReq{
		requestID: req.RequestID,
		sessionID: sessionID,
		response:  make(chan *types.StepDecisionResponse, 1),
		createdAt: time.Now(),
	}
	qe.stepDecisionMu.Lock()
	qe.pendingStepDecisions[req.RequestID] = pending
	qe.stepDecisionMu.Unlock()
	defer func() {
		qe.stepDecisionMu.Lock()
		delete(qe.pendingStepDecisions, req.RequestID)
		qe.stepDecisionMu.Unlock()
	}()

	if out != nil {
		select {
		case out <- types.EngineEvent{
			Type:         types.EngineEventStepDecisionRequested,
			AgentID:      req.AgentID,
			StepDecision: req,
		}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case resp := <-pending.response:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitStepDecision routes a user's step-decision reply back to the
// coordinator that's blocked in requestStepDecision. Mirrors
// SubmitPlanResponse: stale / unknown request_id is a warn, not an
// error (client may retry; engine no longer cares about that decision).
func (qe *QueryEngine) SubmitStepDecision(_ context.Context, _ string, resp *types.StepDecisionResponse) error {
	if resp == nil || resp.RequestID == "" {
		return fmt.Errorf("step decision response: missing request_id")
	}
	qe.stepDecisionMu.Lock()
	pending, ok := qe.pendingStepDecisions[resp.RequestID]
	if ok {
		delete(qe.pendingStepDecisions, resp.RequestID)
	}
	qe.stepDecisionMu.Unlock()
	if !ok {
		qe.logger.Warn("step decision response for unknown request_id (stale or duplicate)",
			zap.String("request_id", resp.RequestID),
		)
		return nil
	}
	pending.response <- resp
	return nil
}

// resolveCoordinator picks the L2 Coordinator for this spawn. preference is
// the raw mode string from SpawnConfig; empty values trigger the B-mode
// ModeSelector which decides between ReAct (default) and Plan based on
// the task's shape.
//
// Resolution order:
//  1. preference is a known mode → use it directly (operator override)
//  2. ModeSelector picks based on goal heuristics (B-mode of B+D scheme)
//  3. Selected mode resolved through registry; unknown modes fall back
//     to ReAct via registry policy
//
// Builds a fresh SharedDeps with task-scoped singletons:
//   - BudgetTracker, Judge, Planner, Fallback: as before
//   - ModeSelector: HeuristicModeSelector default
//
// Exposed as a method (not a free function) so tests can override the
// mode selector by reaching into the dependency, and so the deps
// construction stays close to where coordinators are actually used.
func (qe *QueryEngine) resolveCoordinator(preference, goal string, logger *zap.Logger) Coordinator {
	deps := &SharedDeps{
		QE:           qe,
		Logger:       logger,
		Budget:       NewBudgetTracker(DefaultPlanBudget()).Start(),
		Judge:        NewJudge(logger),
		Planner: NewLLMPlanner(qe.provider, qe.plannerModel()).
			WithRetry(qe.retryer, qe.llmTimeouts(), logger),
		Fallback:     NewFallbackChain(logger),
		ModeSelector: NewHeuristicModeSelector(),
		// LLM-driven dispatch: each plan step gets one structured-output
		// LLM call to pick the best sub-agent from available, with the
		// keyword-based heuristic as fallback for nil-provider /
		// network-error / out-of-set-pick paths. The extra per-step LLM
		// cost is small (256 max tokens, single tool call) and the
		// alternative — keyword-table guessing — silently mis-routes
		// research tasks to developer when text contains "代码" / "脚本" /
		// "code" substrings.
		SubagentResolver: NewLLMSubagentResolver(
			qe.provider,
			qe.plannerModel(),
			qe.defRegistry,
			NewHeuristicSubagentResolver(),
			logger,
		).WithRetry(qe.retryer, qe.llmTimeouts()),
	}

	// B-mode: when no explicit preference, consult the selector. The
	// selector itself respects operator overrides via ExplicitMode if
	// the caller chose to thread one through; otherwise it heuristics.
	chosen := CoordinatorMode(preference)
	if !chosen.IsKnown() {
		var skills []string
		if qe.defRegistry != nil {
			for _, l := range qe.defRegistry.ListForPlanner() {
				skills = append(skills, l.Name)
			}
		}
		out := deps.ModeSelector.Select(nil, ModeSelectorInput{
			Goal:            goal,
			AvailableSkills: skills,
		})
		chosen = out.Mode
		logger.Info("mode selected by selector",
			zap.String("mode", chosen.String()),
			zap.String("reason", out.Reason),
		)
	}

	return qe.coordinators.Resolve(chosen, deps)
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

// SetDefRegistry configures the agent definition registry and initializes
// the @-mention parser for routing user messages to specialized agents.
func (qe *QueryEngine) SetDefRegistry(reg *agent.AgentDefinitionRegistry) {
	qe.defRegistry = reg
	qe.mentionParser = NewMentionParser(reg)
}

// SetArtifactStore configures the artifact backing store. Pass an
// *artifact.Store; kept as `any` so the engine package doesn't import
// internal/artifact (the import only flows the other way through the
// tool layer).
func (qe *QueryEngine) SetArtifactStore(store any) {
	qe.artifactStore = store
}

// SetStatsRegistry wires the session-metrics registry so the engine
// can attribute LLM/sub-agent/tool activity to the correct Tracker.
// nil disables stats wiring.
func (qe *QueryEngine) SetStatsRegistry(r *sessionstats.Registry) {
	qe.statsRegistry = r
}

// SetSessionManager wires the session manager so trace lifecycle
// transitions can force-flush stats to disk.
func (qe *QueryEngine) SetSessionManager(m *session.Manager) {
	qe.sessionManager = m
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
	// context so deeply-nested tools (Orchestrate, Specialists, Agent)
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
		if qe.sessionManager != nil {
			qe.sessionManager.FlushStats(qCtx, sess.ID)
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

// truncateForDisplay clips a string to n runes for safe inclusion in a
// Display.Summary or .Title field. The "…" suffix signals truncation.
func truncateForDisplay(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// extractMessageText extracts the text content from a message's content blocks.
func extractMessageText(msg *types.Message) string {
	var buf strings.Builder
	for _, block := range msg.Content {
		if block.Type == types.ContentTypeText {
			buf.WriteString(block.Text)
		}
	}
	return buf.String()
}

// processWithAgent handles a user message routed to a specific agent via @-mention.
// It emits an agent.routed event and delegates to SpawnSync or a team workflow.
func (qe *QueryEngine) processWithAgent(
	ctx context.Context,
	sessionID string,
	sess *session.Session,
	mention *MentionResult,
	def *agent.AgentDefinition,
) (<-chan types.EngineEvent, error) {
	out := make(chan types.EngineEvent, 64)

	go func() {
		defer close(out)

		// Emit agent.routed event for client observability.
		out <- types.EngineEvent{
			Type:      types.EngineEventAgentRouted,
			AgentName: def.Name,
			AgentDesc: def.Description,
		}

		prompt := mention.Prompt
		if def.SystemPrompt != "" {
			prompt = def.SystemPrompt + "\n\n" + mention.Prompt
		}

		if def.AutoTeam && len(def.SubAgents) > 0 {
			// Team mode: run as coordinator with predefined sub-agents.
			// TODO: implement runTeamWorkflow for auto-team agents.
			qe.logger.Info("team workflow not yet implemented, falling back to single agent",
				zap.String("agent", def.Name),
			)
		}

		// Single agent mode: SpawnSync directly.
		//
		// SubagentType MUST be def.Name (the registry key), not def.Profile
		// (a prompt-profile selector). SpawnSync uses cfg.SubagentType to
		// look the AgentDefinition back up in the registry — passing the
		// profile name silently misses the lookup, which then makes
		// EffectiveTier() return the default TierCoordinator and bypass the
		// L3 driver routing. Specialists / Agent tools already pass def.Name;
		// this @-mention path was the odd one out.
		cfg := &agent.SpawnConfig{
			Prompt:          prompt,
			AgentType:       def.AgentType,
			SubagentType:    def.Name,
			Name:            def.Name,
			Description:     def.Description,
			Model:           def.Model,
			MaxTurns:        def.MaxTurns,
			ParentSessionID: sessionID,
			ParentOut:       out, // enable real-time event forwarding
		}

		result, err := qe.SpawnSync(ctx, cfg)

		if err != nil {
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{
				Type:     types.EngineEventDone,
				Terminal: &types.Terminal{Reason: types.TerminalModelError, Message: err.Error()},
				Usage:    &types.Usage{},
			}
			return
		}

		// Text was already streamed in real-time via ParentOut forwarding.
		// Only emit the final done event.
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: result.Terminal,
			Usage:    result.Usage,
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

	// Register a mailbox for this session so async sub-agents can send
	// completion notifications back. Deregister on exit.
	var mailbox *agent.Mailbox
	if qe.messageBroker != nil {
		mailbox = qe.messageBroker.Register(sess.ID, "")
		defer qe.messageBroker.Unregister(sess.ID)
	}

	// Build the ToolPool once per query loop from the registry.
	pool := tool.NewToolPool(qe.registry, nil /*mcpTools*/, nil /*denyRules*/)

	// Restrict the main-agent tool palette when configured. L1Engine uses
	// this to expose only delegation tools (Agent, Orchestrate) — sub-agents
	// keep the full palette via the SpawnSync path, which builds its own
	// pool independently in subagent.go.
	if len(qe.config.MainAgentAllowedTools) > 0 {
		pool = pool.FilterByNames(qe.config.MainAgentAllowedTools)
	}

	// Create the approval function that sends permission requests to the client via `out`.
	approvalFn := func(ctx context.Context, evtOut chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
		return qe.requestPermissionApproval(ctx, evtOut, sess.ID, req)
	}
	executor := NewToolExecutor(pool, qe.permChecker, qe.logger, qe.config.ToolTimeout, approvalFn)
	if qe.artifactStore != nil {
		executor.SetArtifactStore(qe.artifactStore)
	}
	if qe.statsRegistry != nil {
		executor.SetStatsRegistry(qe.statsRegistry)
	}
	producer := tool.ArtifactProducer{
		AgentID:   "main",
		SessionID: sess.ID,
	}
	if tc := emit.FromContext(ctx); tc != nil {
		producer.TraceID = tc.TraceID
	}
	executor.SetArtifactProducer(producer)

	for {
		ls.turn++

		// ---- Phase 1: Preprocess ----
		messages := sess.GetMessages()

		// Auto-compact if needed.
		if qe.compactor != nil && qe.compactor.ShouldCompact(messages, qe.contextWindow(), qe.config.AutoCompactThreshold) {
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

		// Check max turns. The main-agent loop honours MainAgentMaxTurns
		// when set (L1Engine uses it to enforce a small L1 loop); sub-agents
		// continue to use MaxTurns directly via runSubAgentLoop.
		mainMax := qe.config.MaxTurns
		if qe.config.MainAgentMaxTurns > 0 {
			mainMax = qe.config.MainAgentMaxTurns
		}
		if ls.turn > mainMax {
			return types.Terminal{
				Reason:  types.TerminalMaxTurns,
				Message: fmt.Sprintf("reached max turns (%d)", mainMax),
				Turn:    ls.turn - 1,
			}
		}

		// Build the LLM request.
		// Build structured system prompt using prompt builder.
		// Skill listing is now injected as a system prompt section (Layer 2),
		// replacing the previous per-turn <system-reminder> user message (Layer 3).
		systemPrompt := qe.buildSystemPrompt(ctx, sess, messages)

		req := &provider.ChatRequest{
			Messages:      messages,
			System:        systemPrompt,
			Tools:         pool.Schemas(),
			MaxTokens:     qe.config.MaxTokens,
			ContextWindow: qe.contextWindow(),
		}

		// --- LLM request observability: dump what we're sending to the model ---
		qe.logger.Debug("========== LLM REQUEST DUMP START ==========",
			zap.String("session_id", sess.ID),
			zap.Int("turn", ls.turn),
			zap.Int("message_count", len(messages)),
			zap.Int("tool_schema_count", len(pool.Schemas())),
			zap.Int("system_prompt_len", len(systemPrompt)),
			zap.Int("max_tokens", qe.config.MaxTokens),
		)
		for i, m := range messages {
			contentPreview := ""
			for _, cb := range m.Content {
				if cb.Type == types.ContentTypeText && len(cb.Text) > 0 {
					preview := cb.Text
					if len(preview) > 200 {
						preview = preview[:200] + "...[truncated]"
					}
					contentPreview = preview
					break
				}
				if cb.Type == types.ContentTypeToolUse {
					contentPreview = fmt.Sprintf("[tool_use: %s]", cb.ToolName)
					break
				}
				if cb.Type == types.ContentTypeToolResult {
					preview := cb.ToolResult
					if len(preview) > 100 {
						preview = preview[:100] + "...[truncated]"
					}
					contentPreview = fmt.Sprintf("[tool_result: %s] %s", cb.ToolName, preview)
					break
				}
			}
			qe.logger.Debug("llm request message",
				zap.Int("index", i),
				zap.String("role", string(m.Role)),
				zap.Int("content_blocks", len(m.Content)),
				zap.Int("tokens", m.Tokens),
				zap.String("preview", contentPreview),
			)
		}
		qe.logger.Debug("========== LLM REQUEST DUMP END ==========")

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

		// ---- Phase 2: LLM Call with retry ----
		// L1 main loop's agent identity is "main" — matches the
		// envelope.agent_id stamp on every emitter the main session
		// produces, so heartbeats route to the active turn / message
		// card.
		llmResult := retryLLMCall(ctx, qe.provider, req, qe.logger, qe.retryer, qe.llmTimeouts(), "main", out)

		if llmResult.streamErr != nil {
			// ---- Phase 3: Error Recovery (all retries exhausted) ----
			llmErr := llmResult.streamErr
			out <- types.EngineEvent{Type: types.EngineEventError, Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageDelta, StopReason: "error", Error: llmErr}
			out <- types.EngineEvent{Type: types.EngineEventMessageStop}

			if ctx.Err() != nil {
				return types.Terminal{Reason: types.TerminalAbortedStreaming, Message: "query cancelled", Turn: ls.turn}
			}
			qe.logger.Error("LLM call failed after retries",
				zap.String("session_id", sess.ID),
				zap.Int("turn", ls.turn),
				zap.Error(llmErr),
			)
			return types.Terminal{Reason: types.TerminalModelError, Message: llmErr.Error(), Turn: ls.turn}
		}

		// Events were already streamed in real-time by retryLLMCall.
		// Extract collected results for session state.
		textBuf := llmResult.textBuf
		toolCalls := llmResult.toolCalls

		ls.stopReason = llmResult.stopReason
		if llmResult.lastUsage != nil {
			ls.lastUsage = llmResult.lastUsage
			ls.cumulativeUsage.InputTokens += llmResult.lastUsage.InputTokens
			ls.cumulativeUsage.OutputTokens += llmResult.lastUsage.OutputTokens
			ls.cumulativeUsage.CacheRead += llmResult.lastUsage.CacheRead
			ls.cumulativeUsage.CacheWrite += llmResult.lastUsage.CacheWrite
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

		// Append assistant message to session. Reasoning is threaded
		// through so providers that require thinking-mode replay
		// (DeepSeek) see it on the next request.
		assistantMsg := buildAssistantMessage(textBuf, toolCalls, ls.lastUsage, llmResult.reasoning)
		sess.AddMessage(assistantMsg)

		// ---- Phase 5 (part A): Check terminal — no tool calls means LLM is done. ----
		if len(toolCalls) == 0 {
			// Before terminating, check if there are async sub-agents still
			// running for this session. If so, wait for their completion
			// notifications via the mailbox, inject them as user messages,
			// and continue the loop so the LLM can process the results.
			if qe.shouldWaitForAsyncAgents(sess.ID, mailbox) {
				if msg := qe.waitForMailboxMessage(ctx, sess.ID, mailbox, out); msg != nil {
					sess.AddMessage(*msg)
					continue
				}
			}
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

		// Per-tool routing: split the batch into client-routed and
		// server-routed groups. A tool that implements ClientRoutedTool
		// (e.g. AskUserQuestion) ALWAYS goes to the client. Everything
		// else follows the global ClientTools flag — true means
		// delegate-everything (Claude Code CLI mode), false means
		// run-server-side (web UI mode).
		results := qe.dispatchToolBatch(ctx, executor, pool, toolCalls, out)

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

// dispatchToolBatch routes each tool call to either the client (via
// tool.call) or the server-side executor based on per-tool policy:
//
//   - Tools implementing tool.ClientRoutedTool with IsClientRouted()=true
//     ALWAYS go to the client (e.g. AskUserQuestion — only the UI can ask
//     a human).
//   - All other tools follow the global QueryEngineConfig.ClientTools
//     flag: true delegates everything (CLI mode), false runs server-side
//     (web UI mode where the server has API keys / sub-agent capability).
//
// The returned slice preserves the original toolCalls order so the LLM
// sees results aligned with its tool_use indices.
func (qe *QueryEngine) dispatchToolBatch(
	ctx context.Context,
	executor *ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	if len(toolCalls) == 0 {
		return nil
	}

	results := make([]types.ToolResult, len(toolCalls))

	// Partition by routing policy. Indices into the original slice are
	// preserved so we can stitch results back in order.
	var clientCalls, serverCalls []types.ToolCall
	var clientIdx, serverIdx []int
	for i, tc := range toolCalls {
		if qe.routeToClient(pool, tc.Name) {
			clientCalls = append(clientCalls, tc)
			clientIdx = append(clientIdx, i)
		} else {
			serverCalls = append(serverCalls, tc)
			serverIdx = append(serverIdx, i)
		}
	}

	// Run server-side batch first — it's typically the long-running side
	// (network calls, sub-agent spawns) and starting it before blocking
	// on the client lets the two run in parallel from goroutines started
	// inside ExecuteBatch.
	if len(serverCalls) > 0 {
		serverResults := executor.ExecuteBatch(ctx, serverCalls, out)
		for j, r := range serverResults {
			results[serverIdx[j]] = r
		}
	}

	if len(clientCalls) > 0 {
		clientResults := qe.executeClientTools(ctx, pool, clientCalls, out)
		for j, r := range clientResults {
			results[clientIdx[j]] = r
		}
	}

	return results
}

// routeToClient decides whether a tool call should be sent to the
// connected client (true) or executed server-side (false).
func (qe *QueryEngine) routeToClient(pool *tool.ToolPool, toolName string) bool {
	// Per-tool override: tools that can ONLY work client-side opt in via
	// ClientRoutedTool. This bypasses the global flag — even with
	// ClientTools=false (web UI mode), AskUserQuestion still goes client.
	if t := pool.Get(toolName); t != nil {
		if cr, ok := t.(tool.ClientRoutedTool); ok && cr.IsClientRouted() {
			return true
		}
	}
	// Otherwise honour the global flag.
	return qe.config.ClientTools
}

// executeClientTools sends tool.call events to the client and waits for
// tool.result responses. Wait semantics depend on the tool kind:
//
//   - Human-interactive tools (ClientRoutedTool, e.g. AskUserQuestion):
//     wait INDEFINITELY for the user. The only exits are session abort
//     (ctx cancelled) or the client returning a result. Applying an
//     artificial timeout to a human-in-the-loop call would silently
//     drop the user's pending answer — same reasoning as
//     requestPermissionApproval (§6.4.3).
//
//   - Delegation tools (CLI mode, ClientTools=true): apply
//     QueryEngineConfig.ToolTimeout so a crashed or hung client doesn't
//     pin the engine forever. The client can call tool.progress to
//     reset the timer for legitimately long operations.
//
// The pool is needed to look each tool up and check its routing. Calls
// for tools missing from the pool fall through to the delegation
// (timed) branch — defensive default.
func (qe *QueryEngine) executeClientTools(
	ctx context.Context,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	results := make([]types.ToolResult, len(toolCalls))

	// Register pending tool calls and remember per-call wait policy.
	pendingChs := make([]chan *types.ToolResultPayload, len(toolCalls))
	humanInteractive := make([]bool, len(toolCalls))
	for i, tc := range toolCalls {
		ch := make(chan *types.ToolResultPayload, 1)
		pendingChs[i] = ch
		qe.toolMu.Lock()
		qe.pendingTools[tc.ID] = &pendingToolCall{resultCh: ch}
		qe.toolMu.Unlock()

		if t := pool.Get(tc.Name); t != nil {
			if cr, ok := t.(tool.ClientRoutedTool); ok && cr.IsClientRouted() {
				humanInteractive[i] = true
			}
		}

		// Emit tool.call event to the client.
		out <- types.EngineEvent{
			Type:      types.EngineEventToolCall,
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		}
	}

	// Wait for all results. Human-interactive calls don't have a timeout
	// branch in their select — the user gets all the time they need.
	for i, tc := range toolCalls {
		if humanInteractive[i] {
			select {
			case <-ctx.Done():
				results[i] = types.ToolResult{
					Content: "execution cancelled",
					IsError: true,
				}
			case payload := <-pendingChs[i]:
				results[i] = toolResultFromPayload(payload)
			}
		} else {
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

// cumulativeUsageFor returns the running token totals for sessionID by
// reading the sessionstats Tracker (the single in-memory truth source).
// Falls back to a zero Usage when stats are not wired in (tests).
func (qe *QueryEngine) cumulativeUsageFor(sessionID string) types.Usage {
	if qe == nil || qe.statsRegistry == nil {
		return types.Usage{}
	}
	tr := qe.statsRegistry.Get(sessionID)
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

// buildAssistantMessage creates a Message from the LLM's streamed output.
// reasoning is the thinking-mode chain-of-thought (DeepSeek / o1 / xAI);
// preserved on the Message so the bifrost adapter can replay it on the
// next turn — DeepSeek thinking models reject requests where it's absent.
func buildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage, reasoning string) types.Message {
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
		Role:             types.RoleAssistant,
		Content:          content,
		CreatedAt:        time.Now(),
		Tokens:           tokens,
		ReasoningContent: reasoning,
	}
}

// getSkillListing returns the cached skill listing string.
// Computed once on first call using FormatCommandsWithinBudget (lazy init).
// The listing is passed into PromptContext.SkillListing for the SkillsSection to render.
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

// getSkillListingFiltered returns the skill listing filtered by an allowed set.
// When allowedSkills is nil, returns the full listing (same as getSkillListing).
// When non-nil, only skills whose names are in the map are included.
func (qe *QueryEngine) getSkillListingFiltered(allowedSkills map[string]bool) string {
	if allowedSkills == nil {
		return qe.getSkillListing()
	}
	if qe.cmdRegistry == nil {
		return ""
	}
	allCmds := qe.cmdRegistry.GetSkillToolCommands()
	if len(allCmds) == 0 {
		return ""
	}
	filtered := make([]*command.PromptCommand, 0, len(allowedSkills))
	for _, cmd := range allCmds {
		if allowedSkills[cmd.Name] {
			filtered = append(filtered, cmd)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return skilltool.FormatCommandsWithinBudget(filtered, 200000)
}

// buildSystemPrompt constructs the system prompt using the prompt builder.
// Uses per-session whole-output caching: only rebuilds when inputs change.
// Falls back to config.SystemPrompt if builder fails.
func (qe *QueryEngine) buildSystemPrompt(ctx context.Context, sess *session.Session, messages []types.Message) string {
	// If prompt builder is not initialized, use static prompt
	if qe.promptBuilder == nil {
		return qe.config.SystemPrompt
	}

	// Estimate tokens used by conversation
	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	// Compute budget early for cache comparison
	budget := prompt.ComputeSystemPromptBudget(200000, totalTokens, 16384, prompt.DefaultSafetyMargin)

	// Check if we have a valid cached prompt for this session
	qe.promptCacheMu.RLock()
	cached := qe.promptCache[sess.ID]
	qe.promptCacheMu.RUnlock()

	if cached != nil {
		// Determine if cache is still valid:
		// - budget hasn't dropped by more than 10% (no section would be skipped)
		// - task state hasn't changed
		// - memory hasn't changed
		// - date hasn't changed (cross-midnight)
		today := time.Now().Format("2006-01-02")
		budgetDrift := float64(cached.budget-budget) / float64(cached.budget)
		hasTask := false // TODO: populate from session metadata when available
		memoryLen := 0   // TODO: populate when memory loading is implemented

		if budgetDrift < 0.1 && cached.hasTask == hasTask && cached.memoryLen == memoryLen && cached.date == today {
			qe.logger.Debug("prompt cache hit",
				zap.String("session_id", sess.ID),
				zap.String("version", cached.output.Version),
				zap.Int("budget_cached", cached.budget),
				zap.Int("budget_current", budget),
			)
			return cached.prompt
		}

		qe.logger.Debug("prompt cache invalidated",
			zap.String("session_id", sess.ID),
			zap.Float64("budget_drift", budgetDrift),
			zap.Bool("task_changed", cached.hasTask != hasTask),
			zap.Bool("memory_changed", cached.memoryLen != memoryLen),
			zap.Bool("date_changed", cached.date != today),
		)
	}

	// Cache miss or invalidated — full build
	promptCtx := &prompt.PromptContext{
		SessionID:         sess.ID,
		Turn:              len(messages),
		Session:           sess,
		Tools:             qe.registry,
		TotalTokensUsed:   totalTokens,
		ContextWindowSize: qe.contextWindow(),
		Memory:            make(map[string]string),
		EnvInfo:           qe.getEnvSnapshot(),
		SkillListing:      qe.getSkillListing(),
		TeamMembers:       qe.getTeamMembers(),
	}

	activeProfile := qe.promptProfile

	output, err := qe.promptBuilder.Build(promptCtx, activeProfile)
	if err != nil {
		qe.logger.Error("prompt build failed, using fallback",
			zap.Error(err),
			zap.String("session_id", sess.ID),
		)
		return qe.config.SystemPrompt
	}

	// --- Prompt observability: dump full prompt structure ---
	qe.logger.Debug("========== PROMPT DUMP START ==========")
	qe.logger.Debug(output.Dump())
	qe.logger.Debug("========== PROMPT DUMP END ==========",
		zap.String("session_id", sess.ID),
		zap.Int("turn", promptCtx.Turn),
		zap.String("version", output.Version),
		zap.Int("total_tokens", output.Metadata.TotalTokens),
		zap.Int("budget", output.Metadata.TokenBudget),
		zap.Int("block_count", len(output.Blocks)),
		zap.Int("skipped_count", len(output.Metadata.SkippedSections)),
		zap.Float64("cacheable_ratio", output.Metadata.CacheMetrics.CacheableRatio),
	)
	for _, b := range output.Blocks {
		qe.logger.Debug("prompt block",
			zap.String("section", b.Name),
			zap.Int("tokens", b.EstimatedTokens),
			zap.Bool("cacheable", b.Cacheable),
			zap.Int("content_len", len(b.Content)),
		)
	}
	for _, s := range output.Metadata.SkippedSections {
		qe.logger.Debug("prompt section skipped",
			zap.String("section", s.Section),
			zap.String("reason", s.Reason),
		)
	}

	// Log the final system prompt text sent to the LLM.
	result := output.ToSystemPrompt()
	qe.logger.Debug("========== FINAL SYSTEM PROMPT START ==========\n" + result + "\n========== FINAL SYSTEM PROMPT END ==========",
		zap.String("session_id", sess.ID),
		zap.Int("char_count", len(result)),
		zap.Int("estimated_tokens", prompt.EstimateTokens(result)),
	)

	// Cache the result for this session
	qe.promptCacheMu.Lock()
	qe.promptCache[sess.ID] = &promptCacheEntry{
		prompt:    result,
		output:    output,
		budget:    budget,
		hasTask:   false, // TODO: update when task state is populated
		memoryLen: 0,     // TODO: update when memory is populated
		date:      time.Now().Format("2006-01-02"),
	}
	qe.promptCacheMu.Unlock()

	return result
}

// getTeamMembers builds the dynamic team member list from the agent definition registry.
func (qe *QueryEngine) getTeamMembers() []prompt.TeamMember {
	if qe.defRegistry == nil {
		return nil
	}
	defs := qe.defRegistry.TeamMembers()
	members := make([]prompt.TeamMember, 0, len(defs))
	for _, d := range defs {
		members = append(members, prompt.TeamMember{
			DisplayName: d.DisplayName,
			CodeName:    d.Name,
			Description: d.Description,
			Personality: d.Personality,
			Triggers:    d.Triggers,
		})
	}
	return members
}

// getEnvSnapshot captures current environment information dynamically.
func (qe *QueryEngine) getEnvSnapshot() prompt.EnvSnapshot {
	snap := prompt.EnvSnapshot{
		OS:       runtime.GOOS,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Date:     time.Now().Format("2006-01-02"),
	}

	// CWD
	snap.CWD = "~/.harnessclaw/workspace"

	// Shell
	if shell := os.Getenv("SHELL"); shell != "" {
		snap.Shell = shell
	} else if comspec := os.Getenv("COMSPEC"); comspec != "" {
		snap.Shell = comspec
	}

	return snap
}

// shouldWaitForAsyncAgents returns true if the query loop should block on
// the mailbox instead of terminating. This happens when there are async
// sub-agents still running for this session.
func (qe *QueryEngine) shouldWaitForAsyncAgents(sessionID string, mailbox *agent.Mailbox) bool {
	if mailbox == nil || qe.agentRegistry == nil {
		return false
	}
	// Check if any async agents spawned by this session are still running.
	return qe.agentRegistry.HasRunningForParent(sessionID)
}

// waitForMailboxMessage blocks until a message arrives on the mailbox or the
// context is cancelled. It converts the AgentMessage into a types.Message
// (user role) so the LLM can process the notification in the next turn.
// Returns nil if the context is cancelled before a message arrives.
func (qe *QueryEngine) waitForMailboxMessage(
	ctx context.Context,
	sessionID string,
	mailbox *agent.Mailbox,
	out chan<- types.EngineEvent,
) *types.Message {
	if mailbox == nil {
		return nil
	}

	qe.logger.Info("waiting for async agent notifications",
		zap.String("session_id", sessionID),
	)

	select {
	case <-ctx.Done():
		return nil
	case agentMsg, ok := <-mailbox.Receive():
		if !ok || agentMsg == nil {
			return nil
		}
		qe.logger.Info("received async agent notification",
			zap.String("session_id", sessionID),
			zap.String("from", agentMsg.From),
			zap.String("type", string(agentMsg.Type)),
		)

		// Format the notification as a user message for the LLM.
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
