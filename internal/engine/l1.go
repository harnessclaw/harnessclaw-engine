package engine

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// L1Engine is the user-facing entry point — the only engine reachable from
// the WebSocket / API channel. It owns the L1 layer (面子): persona,
// lightweight Q&A, clarification, dispatch decision. It does NOT execute
// file/bash/grep tools directly; those live in L2 sub-agents spawned via
// the Agent and orchestrate tools.
//
// Implementation note: L1Engine is a thin wrapper over QueryEngine. The
// underlying loop, LLM calling, session management, and prompt assembly
// stay in QueryEngine — duplicating them would only double the bug surface.
// L1's "smallness" is enforced via configuration:
//
//   - Profile          → EmmaProfile (identity / principles / memory — no team:
//                        L1 treats L2 as a black box; the team roster is
//                        consumed by the scheduler, not by emma)
//   - DisplayName      → "emma" (interpolated into worker identity templates)
//   - AllowedTools     → ["scheduler", ...] (delegation + light context tools)
//   - MaxTurns         → 10 (small loop, chat-oriented)
//
// L2 sub-agents (workers, planner, etc.) bypass L1 restrictions because
// SpawnSync builds its own ToolPool from the registry independently.
type L1Engine struct {
	inner  *QueryEngine
	config L1Config
	logger *zap.Logger
}

// L1Config configures the L1 wrapper. All fields have sensible defaults; an
// empty L1Config is valid and produces the canonical emma setup.
type L1Config struct {
	// Profile is the prompt profile used for the L1 main agent. Default:
	// prompt.EmmaProfile.
	Profile *prompt.AgentProfile

	// DisplayName is the friendly leader name interpolated into worker
	// identity prompts (e.g., "你叫小林，是 emma 团队的搭档"). Default: "emma".
	DisplayName string

	// AllowedTools restricts the tools advertised to the L1 LLM. Default:
	// ["Agent", "orchestrate"]. Pass an explicit slice to override; pass
	// a single-element slice to a non-default tool list to widen/narrow.
	AllowedTools []string

	// MaxTurns caps the L1 loop. Default: 10. Sub-agents are NOT affected
	// — they derive their cap from QueryEngineConfig.MaxTurns.
	MaxTurns int
}

// DefaultL1Config returns the canonical emma L1 configuration.
//
// Tool palette rationale (post 3-tier refactor):
//   - scheduler               → THE delegation entry point. emma never picks
//                              between single-step / multi-step or specific
//                              sub-agents — the scheduler (L2) handles all
//                              decomposition, dispatch, and integration.
//   - web_search / tavily_search → emma's own *light* fact-finding for
//                              context gathering before dispatching to the
//                              scheduler (e.g. confirm an entity, fetch a
//                              key fact). Deep research is the scheduler's
//                              concern, not emma's.
//   - ask_user_question        → clarification when the request is ambiguous
//                              or a key fact is missing. Prefer asking
//                              over guessing — the scheduler cannot ask the
//                              user, so emma must clarify upstream.
//
// The task tool is intentionally NOT in this list. It lives inside the L2
// layer (the scheduler uses task internally to dispatch L3).
func DefaultL1Config() L1Config {
	return L1Config{
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

// NewL1Engine wraps an existing QueryEngine, applying L1 configuration to
// the inner engine. After construction the inner engine's config reflects
// the L1 settings — callers must NOT mutate inner.config externally.
//
// Construction order:
//  1. Build QueryEngine (engine.NewQueryEngine)
//  2. Wrap with NewL1Engine (this function)
//  3. Hand the L1Engine to the router/channel layer
//  4. Register the task / scheduler tools using the inner QueryEngine as
//     the AgentSpawner — they call SpawnSync directly to launch L2/L3 workers.
func NewL1Engine(inner *QueryEngine, cfg L1Config, logger *zap.Logger) *L1Engine {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Apply defaults for unset fields.
	if cfg.Profile == nil {
		cfg.Profile = prompt.EmmaProfile
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "emma"
	}
	if len(cfg.AllowedTools) == 0 {
		// Default to the canonical L1 palette — single delegation entry
		// (scheduler) plus light context tools. Mirror DefaultL1Config()
		// so that NewL1Engine(inner, L1Config{}) behaves identically to
		// NewL1Engine(inner, DefaultL1Config()).
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

	// Apply L1 settings to the inner QueryEngine. This MUST happen before
	// the engine processes any message; the L1Engine owns these fields
	// from this point on.
	inner.config.MainAgentProfile = cfg.Profile
	inner.config.MainAgentDisplayName = cfg.DisplayName
	inner.config.MainAgentAllowedTools = cfg.AllowedTools
	inner.config.MainAgentMaxTurns = cfg.MaxTurns
	inner.promptProfile = cfg.Profile

	return &L1Engine{
		inner:  inner,
		config: cfg,
		logger: logger,
	}
}

// Config returns a copy of the active L1 configuration. Useful for tests
// and observability.
func (l *L1Engine) Config() L1Config {
	cfg := l.config
	if l.config.AllowedTools != nil {
		cfg.AllowedTools = append([]string(nil), l.config.AllowedTools...)
	}
	return cfg
}

// Inner exposes the underlying QueryEngine so the wiring layer can pass it
// to Agent / orchestrate tools as the AgentSpawner. Sub-agent spawning
// bypasses L1 — that is by design (L2 workers need the full tool palette).
func (l *L1Engine) Inner() *QueryEngine {
	return l.inner
}

// --- engine.Engine implementation: every method is a passthrough. ---

// ProcessMessage delegates to the inner QueryEngine, which now runs with
// the L1 profile, restricted tool palette, and small loop cap installed at
// construction time.
//
// Side effect: idempotently bootstraps the on-disk workspace
// ({rootDir}/session/{sessionID}/{plan.json,tasks/,deliverables/}) so any
// plan_update / meta_write / promote dispatched downstream finds the layout
// already in place. Failure here is logged but not fatal — the downstream
// tools surface the real error if the dir genuinely cannot be written.
func (l *L1Engine) ProcessMessage(
	ctx context.Context,
	sessionID string,
	msg *types.Message,
) (<-chan types.EngineEvent, error) {
	if root := workspaceRootDir(); root != "" {
		if err := workspace.EnsureSession(root, sessionID); err != nil {
			l.logger.Warn("ensure session workspace",
				zap.String("session_id", sessionID),
				zap.Error(err),
			)
		}
	}
	return l.inner.ProcessMessage(ctx, sessionID, msg)
}

// SubmitToolResult forwards a client-side tool result to the inner engine.
// Tool results are currently delivered for the L1 layer's tool calls (Agent,
// orchestrate, ask_user_question if/when added). L2 sub-agent tool results
// flow internally and never cross this method.
func (l *L1Engine) SubmitToolResult(
	ctx context.Context,
	sessionID string,
	result *types.ToolResultPayload,
) error {
	return l.inner.SubmitToolResult(ctx, sessionID, result)
}

// SubmitPermissionResult forwards a permission decision to the inner engine.
func (l *L1Engine) SubmitPermissionResult(
	ctx context.Context,
	sessionID string,
	resp *types.PermissionResponse,
) error {
	return l.inner.SubmitPermissionResult(ctx, sessionID, resp)
}

// SubmitPlanResponse forwards a plan approval decision to the inner engine.
func (l *L1Engine) SubmitPlanResponse(
	ctx context.Context,
	sessionID string,
	resp *types.PlanResponse,
) error {
	return l.inner.SubmitPlanResponse(ctx, sessionID, resp)
}

// SubmitStepDecision forwards a continue/retry/cancel reply to the inner engine.
func (l *L1Engine) SubmitStepDecision(
	ctx context.Context,
	sessionID string,
	resp *types.StepDecisionResponse,
) error {
	return l.inner.SubmitStepDecision(ctx, sessionID, resp)
}

// AbortSession cancels in-flight processing for the given session.
func (l *L1Engine) AbortSession(ctx context.Context, sessionID string) error {
	return l.inner.AbortSession(ctx, sessionID)
}
