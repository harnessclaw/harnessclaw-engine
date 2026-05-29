package spawn

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// Deps is the dependency surface that the parent engine must implement
// so spawn.Spawner can do its work. Kept intentionally narrow — adding
// a new field here means spawner needs access to new engine state.
//
// State accessors give spawn the raw QE fields it used to dereference
// as qe.X; helper methods wrap engine-package free functions and methods
// that spawn would call directly if it lived in the same package.
type Deps interface {
	// --- state accessors ---
	Logger() *zap.Logger

	// SpawnerConfig returns a value snapshot of the engine config fields
	// spawn needs. Named with the Spawner prefix so it doesn't clash
	// with the existing QueryEngine.Config() accessor.
	SpawnerConfig() SpawnConfig

	Provider() provider.Provider
	Registry() *tool.Registry
	CmdRegistry() *command.Registry
	Compactor() compact.Compactor
	PermChecker() permission.Checker

	SessionMgr() *session.Manager
	StatsRegistry() *sessionstats.Registry
	DefRegistry() *agent.AgentDefinitionRegistry
	SkillReader() *skill.Reader

	PromptBuilder() *prompt.Builder
	SchedulerCoord() *enginesched.Coordinator

	// Retryer hands back the shared retry driver used by every LLM
	// call (main loop + sub-agents) so the consecutive-529 fallback
	// counter stays session-wide.
	Retryer() *retry.Retryer

	// SelfSpawner returns the parent AgentSpawner so sub-agents can
	// recursively spawn (e.g., L2 → L3). The parent supplies itself —
	// QueryEngine implements agent.AgentSpawner via SpawnSync.
	SelfSpawner() agent.AgentSpawner

	// --- helper wrappers ---
	// LLMTimeouts returns the per-call deadlines applied to one
	// provider.Chat() round-trip, sourced from the parent engine config.
	LLMTimeouts() LLMTimeouts

	// CallLLM runs one LLM chat round with retries. Mirrors the engine
	// package's callLLM free function so spawn can reuse the exact same
	// retry / heartbeat plumbing.
	CallLLM(
		ctx context.Context,
		req *provider.ChatRequest,
		logger *zap.Logger,
		agentID string,
		out, planningOut chan<- types.EngineEvent,
	) LLMCallResult

	// NewToolExecutor constructs an executor with the engine's standard
	// permission + stats wiring. spawn calls this once per sub-agent run.
	NewToolExecutor(
		pool *tool.ToolPool,
		perm permission.Checker,
		logger *zap.Logger,
		timeout time.Duration,
		approvalFn ToolApprovalFunc,
	) ToolExecutor

	// DispatchToolBatch routes a batch of tool calls through the engine's
	// server/client tool router. Returns per-call results in input order.
	// sess is required so client-routed tools can register pending
	// awaits on session.Awaits.
	DispatchToolBatch(
		ctx context.Context,
		sess *session.Session,
		executor ToolExecutor,
		pool *tool.ToolPool,
		toolCalls []types.ToolCall,
		out chan<- types.EngineEvent,
	) []types.ToolResult

	// BuildAssistantMessage assembles an assistant Message from streamed
	// LLM output. Wraps the engine helper of the same name.
	BuildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage, reasoning string) types.Message

	// EffectiveContextWindow applies the engine's "configured value or
	// fallback" rule. Used by the sub-agent loop's compactor gate.
	EffectiveContextWindow(configured int) int

	// ContextWindow returns the engine's effective context window for
	// the main loop, used when rendering sub-agent prompts.
	ContextWindow() int

	// GetSkillListingFiltered renders the skill catalogue to a prompt
	// fragment, restricted to allowedSkills when non-nil.
	GetSkillListingFiltered(allowedSkills map[string]bool) string

	// GetEnvSnapshot collects EnvSection inputs (cwd, sessionRoot, ...).
	GetEnvSnapshot(sessionRoot string) prompt.EnvSnapshot

	// GetSessionApprovedTools returns the parent session's whitelist of
	// tools the user has already approved. spawn passes this to
	// InheritedChecker for sub-agent permission.
	GetSessionApprovedTools(sessionID string) []string

	// BuildLoadedSkillsBlock renders the <loaded-skills> XML container
	// prepended to the freelancer prompt.
	BuildLoadedSkillsBlock(fulls []*skill.SkillFull) string

	// AgentRegistry holds the parent engine's async-agent registry, used by
	// SpawnAsync to register/track background agents. nil disables async
	// support (SpawnAsync returns ErrNoAgentRegistry).
	AgentRegistry() *agent.AgentRegistry

	// MessageBroker, when non-nil, is the inter-agent notification bus
	// SpawnAsync writes WorkerNotification messages to on completion.
	MessageBroker() *agent.MessageBroker
}

// SpawnConfig is the spawn-relevant subset of emma.Config.
// Returned as a value snapshot from Deps.Config() so spawn code never
// holds a pointer into the engine's mutable state.
type SpawnConfig struct {
	MaxTurns             int
	AutoCompactThreshold float64
	ToolTimeout          time.Duration
	MaxTokens            int
	ContextWindow        int
	SystemPrompt         string
	ClientTools          bool

	MainAgentDisplayName string

	MaxPlanReplans      int
	MaxStepAttempts     int
	LLMMaxRetries       int
	LLMAPITimeout       time.Duration
	LLMFirstByteTimeout time.Duration
}

// LLMTimeouts mirrors engine.llmCallTimeouts so spawn can request and
// thread the same per-attempt deadlines through CallLLM without
// importing the engine package.
type LLMTimeouts struct {
	API       time.Duration
	FirstByte time.Duration
}

// LLMCallResult mirrors the engine's llmCallResult. spawn only reads it.
type LLMCallResult struct {
	TextBuf    string
	ToolCalls  []types.ToolCall
	StopReason string
	LastUsage  *types.Usage
	Reasoning  string
	StreamErr  error
}

// ToolApprovalFunc mirrors engine.PermissionApprovalFunc.
type ToolApprovalFunc func(ctx context.Context, out chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse

// ToolExecutor is the spawn-side handle on engine.ToolExecutor. spawn
// calls these methods after constructing the executor via Deps.NewToolExecutor.
type ToolExecutor interface {
	SetArtifactProducer(p tool.ArtifactProducer)
	SetTaskContract(c tool.TaskContract)
	SetAgentScope(s tool.AgentScope)
}

// Spawner runs sub-agents on behalf of the parent engine. Owns the
// 14-step SpawnSync pipeline and the per-agent taskRegistry cache.
type Spawner struct {
	deps Deps

	// taskRegistry stores completed sub-agent results by agentID.
	// Used for context passing (depends_on) and debugging.
	// TODO: add TTL or LRU eviction to prevent unbounded growth.
	taskRegistryMu sync.RWMutex
	taskRegistry   map[string]*agent.SpawnResult

	// searchGapDetector emits a one-shot per-session CardSystem notice
	// when a TierSubAgent spawns with declared search capability but
	// neither web_search nor tavily_search is registered at runtime.
	searchGapDetector *SearchGapDetector
}

// NewSpawner constructs a Spawner backed by the given Deps. Deps must
// remain valid for the Spawner's lifetime — typically Deps is the
// parent QueryEngine, which lives for the process lifetime.
func NewSpawner(deps Deps) *Spawner {
	return &Spawner{
		deps:              deps,
		taskRegistry:      make(map[string]*agent.SpawnResult),
		searchGapDetector: NewSearchGapDetector(deps.Logger()),
	}
}
