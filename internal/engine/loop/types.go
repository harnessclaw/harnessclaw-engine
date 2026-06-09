// Package loop is the shared LLM run-loop primitive. It knows how to
// drive a turn-by-turn provider conversation (LLM call -> tool dispatch
// -> auto-compaction -> termination check) but knows nothing about
// agent tiers, sub-agent contracts, or wire event envelopes. Tier
// modules build Config and pass an OnTurnComplete TurnHook to express
// their business logic.
package loop

import (
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/toolexec"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// Config is the per-run input. Caller (tier module) is responsible for
// assembling SystemPrompt, Tools pool, Session, and an OnTurnComplete
// hook that encodes the caller's termination & feedback logic.
type Config struct {
	Session      *session.Session
	SystemPrompt string
	Tools        *tool.ToolPool
	Provider     provider.Provider
	Compactor    compact.Compactor
	Retryer      *retry.Retryer
	Logger       *zap.Logger

	// ClientAwaitSession is the user-facing session that receives
	// client-routed tool results. Sub-agent loops can run on their own
	// Session while browser/Electron results return through the root
	// WebSocket session.
	ClientAwaitSession *session.Session

	MaxTurns      int
	MaxTokens     int
	ContextWindow int
	ToolTimeout   time.Duration

	// AutoCompactThreshold is the ratio of ContextWindow at which the
	// Compactor (when set) triggers. Zero means "use the loop default"
	// (0.8). Tier modules with their own token-pressure profile (e.g. L1
	// emma running long-lived sessions) override it via Config.
	AutoCompactThreshold float64

	// LLMAPITimeout caps the wall-clock duration of one LLM Chat call
	// (full request → terminal stream chunk). Zero leaves the watchdog
	// disarmed — appropriate for tests but DANGEROUS in production: a
	// stuck upstream stream (no chunks arriving) blocks the loop
	// forever and the orphan watchdog only catches it 5–10 minutes
	// later via the card layer. Tier modules fill this from
	// emma.Config.LLMAPITimeout so sub-agent loops inherit the same
	// "request_timeout: 900s" semantics that protect the L1 loop.
	LLMAPITimeout time.Duration

	// LLMFirstByteTimeout cancels the call when Chat() returned ok but
	// no stream chunk arrived within this window. Catches "gateway
	// accepted the request but never emits bytes" black-hole failures
	// without limiting overall generation time (the watchdog disarms
	// on the first chunk). Zero disables. Same provenance as
	// LLMAPITimeout.
	LLMFirstByteTimeout time.Duration

	// TaskContract and ArtifactProducer are attached to server-side tool
	// execution contexts. submit_task_result uses TaskContract for
	// structured result validation; artifact-producing tools use the
	// producer stamp for lineage metadata.
	TaskContract     tool.TaskContract
	ArtifactProducer tool.ArtifactProducer

	Out     chan<- types.EngineEvent
	AgentID string

	// PermChecker is the permission gate consulted before every tool call.
	// REQUIRED — passing nil causes runtime panic in toolexec because every
	// tool path calls permChecker.Check unless a PreChecker auto-allows.
	PermChecker permission.Checker

	// ApprovalFn handles permission.Ask decisions. Optional; nil falls back
	// to deny-on-Ask behavior. Sub-agents typically pass nil since they
	// have no UI to surface approval prompts to.
	ApprovalFn toolexec.PermissionApprovalFunc

	// AgentScope is the per-spawn filesystem + identity scope reachable
	// from tool Execute via tool.AgentScopeFromCtx. Tools that resolve
	// workspace-relative paths (meta_write, submit_task_result) require
	// SessionRoot here; tier modules fill it from cfg.RootSessionID +
	// cfg.TaskID + subagent_type before calling loop.Run. Empty is
	// tolerated (legacy "no scope" behavior).
	AgentScope tool.AgentScope

	// OnTurnComplete is invoked at the end of each turn (after assistant
	// message and tool results are produced). It returns a Decision
	// telling the loop whether to stop or to inject extra messages
	// before the next LLM call. Required.
	OnTurnComplete TurnHook

	// Hooks are optional per-turn observation callbacks. All fields are
	// nil-able; nil means "skip this phase". Hooks are observation-only
	// — they cannot influence control flow. Control flow stays in
	// OnTurnComplete.
	//
	// Sub-agent (L3) callers leave this zero. Main-agent (L1) callers
	// use it to drive UI events (MessageStart/MessageDelta/MessageStop/
	// NextRoundThinking) and diagnostic logging (ctx.Cause on LLM error).
	Hooks Hooks
}

// Hooks bundles the optional per-phase observation callbacks the loop
// fires while driving a turn. All fields nil-default. Each hook captures
// its own context via closure; the loop does not pass ctx as a parameter.
type Hooks struct {
	// OnTurnStart fires at the start of each turn, after the ctx
	// liveness check but before auto-compact. Use for "I'm about to
	// start work" markers (e.g. emma's EngineEventMessageStart).
	OnTurnStart func(turn int)

	// OnLLMResponse fires after a successful LLM stream completes and
	// the assistant message has been appended to the session, but
	// BEFORE tool dispatch begins. Use for "the model spoke, here's
	// what it said" events (e.g. emma's MessageDelta + MessageStop).
	OnLLMResponse func(turn int, snap LLMResponseSnapshot)

	// OnLLMError fires when the LLM call fails (nil result or
	// StreamErr). The loop returns immediately after this hook returns
	// — the hook cannot recover the turn, only observe. Use for
	// terminal error events + ctx.Cause diagnostic logging.
	OnLLMError func(turn int, err error)

	// OnToolsDispatched fires after tool dispatch completes, but only
	// when at least one tool was called this turn. Fires before
	// OnTurnComplete. Use for inter-round signalling (e.g. emma's
	// NextRoundThinking event).
	OnToolsDispatched func(turn int, calls []types.ToolCall, results []types.ToolResult)
}

// LLMResponseSnapshot is what the loop hands to Hooks.OnLLMResponse. It
// exposes the fields a tier module typically needs to render
// post-response UI events without re-deriving them from session state.
type LLMResponseSnapshot struct {
	AssistantMsg types.Message
	StopReason   string
	LastUsage    *types.Usage // this turn's raw usage (NOT cumulative)
	Reasoning    string
}

// TurnSnapshot is the read-only view of a finished turn that the loop
// passes to TurnHook. Combines the inputs of the old multi-parameter
// signature plus stopReason/lastUsage/hasToolCalls so hooks can express
// continuation logic without re-deriving them.
type TurnSnapshot struct {
	Turn         int
	AssistantMsg types.Message
	ToolResults  []types.ToolResult
	StopReason   string
	LastUsage    *types.Usage // this turn's raw usage (NOT cumulative)
	HadToolCalls bool
}

// TurnHook is the per-turn callback the loop invokes after each LLM
// round + tool execution. Implementations may hold state (retry
// counters, contract trackers, etc.) via closure or method receiver.
type TurnHook func(snap TurnSnapshot) Decision

// Decision is the value returned by TurnHook. Exactly one of Terminate
// being non-nil OR Inject being non-empty is the typical case; both nil
// means "continue without injection".
type Decision struct {
	// Terminate non-nil stops the loop and is returned as
	// Result.Terminal. Loop appends no further messages.
	Terminate *types.Terminal

	// Inject (when non-empty) is appended to session.messages before
	// the next LLM turn. Use for "retry with correction" patterns:
	// e.g., contract validation injects a synthetic tool_result
	// message describing the schema error.
	Inject []types.Message
}

// Result is what Run returns on natural completion.
type Result struct {
	Terminal        types.Terminal
	Usage           types.Usage
	NumTurns        int
	LastMessage     *types.Message // nil if no assistant message ever produced
	LastToolResults []types.ToolResult
}
