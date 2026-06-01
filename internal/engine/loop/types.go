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
	"harnessclaw-go/internal/engine/toolexec"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tool"
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

	MaxTurns      int
	MaxTokens     int
	ContextWindow int
	ToolTimeout   time.Duration

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

	// OnTurnComplete is invoked at the end of each turn (after assistant
	// message and tool results are produced). It returns a Decision
	// telling the loop whether to stop or to inject extra messages
	// before the next LLM call. Required.
	OnTurnComplete TurnHook
}

// TurnHook is the per-turn callback the loop invokes after each LLM
// round + tool execution. Implementations may hold state (retry
// counters, contract trackers, etc.) via closure or method receiver.
type TurnHook func(turn int, assistantMsg types.Message, toolResults []types.ToolResult) Decision

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
	Terminal    types.Terminal
	Usage       types.Usage
	NumTurns    int
	LastMessage *types.Message // nil if no assistant message ever produced
}
