// Package emitv2 implements the v2.2 UI-first emit protocol described in
// docs/emit/2026-05-07-protocol-v2.2-card.md.
//
// Designed as a clean parallel implementation alongside the v1 internal/emit
// package; v1 stays untouched until the engine is migrated. Wire format is
// equal to the in-memory shape (json.Marshal of Event), so there is no
// separate "mapper" stage — the Builder produces wire-ready events.
//
// Terminology:
//   - Event: a single message on the wire ({type, envelope, hint?, metrics?, payload}).
//   - Card:  a long-lived UI/state object (turn, message, tool, agent, ...) that
//            spans multiple events: card.add → card.set/append/tick* → card.close.
//   - Emitter: a per-agent producer that carries identity (agent_id/role/run)
//              and emits events to a Sink.
package emitv2

import "time"

// EventType is the wire-level action verb. v2.2 has 8 actions across 3
// categories: card.* (state/stream/telemetry), prompt.* (interaction),
// session.event (lifecycle).
type EventType string

const (
	// State actions
	EventCardAdd   EventType = "card.add"
	EventCardSet   EventType = "card.set"
	EventCardClose EventType = "card.close"

	// Stream action (must be ordered, must not be dropped)
	EventCardAppend EventType = "card.append"

	// Telemetry action (may be throttled / dropped)
	EventCardTick EventType = "card.tick"

	// Interaction
	EventPromptUser  EventType = "prompt.user"
	EventPromptReply EventType = "prompt.reply"

	// Lifecycle
	EventSession EventType = "session.event"
)

// Event is the wire-format record. JSON marshalling produces the on-the-wire
// frame directly; there is no intermediate "engine event" type.
type Event struct {
	Type     EventType `json:"type"`
	Envelope Envelope  `json:"envelope"`
	Hint     *Hint     `json:"hint,omitempty"`
	Metrics  *Metrics  `json:"metrics,omitempty"`
	Payload  any       `json:"payload,omitempty"`
}

// Envelope carries identity, causality, ordering and severity. Filled by
// the framework — never by user/LLM code.
type Envelope struct {
	EventID      string    `json:"event_id"`
	SessionID    string    `json:"session_id"`
	TraceID      string    `json:"trace_id"`
	CardID       string    `json:"card_id"`
	ParentCardID string    `json:"parent_card_id,omitempty"`
	CardKind     CardKind  `json:"card_kind"`
	Seq          int64     `json:"seq"`
	Timestamp    time.Time `json:"timestamp"`
	AgentID      string    `json:"agent_id,omitempty"`
	AgentRole    AgentRole `json:"agent_role,omitempty"`
	AgentRunID   string    `json:"agent_run_id,omitempty"`
	Severity     Severity  `json:"severity,omitempty"`
}

// CardKind is the controlled enum of card categories.
type CardKind string

const (
	CardTurn     CardKind = "turn"
	CardMessage  CardKind = "message"
	CardTool     CardKind = "tool"
	CardAgent    CardKind = "agent"
	CardPlan     CardKind = "plan"
	CardStep     CardKind = "step"
	CardArtifact CardKind = "artifact"
	CardThinking CardKind = "thinking"
	CardMemoryOp CardKind = "memory_op"
	CardBudget   CardKind = "budget"
	CardTodo     CardKind = "todo"
	CardTeam     CardKind = "team"
	// CardSystem is the generic system-level notification card — used
	// for framework-emitted notices (capability gaps, configuration
	// warnings, key expirations, etc.). Renderer keys off Hint.Title /
	// Hint.Icon / SystemPayload.Summary so individual notices don't
	// each need their own CardKind.
	CardSystem CardKind = "system"
)

// AgentRole classifies the responsibility of the producing agent. Mirrors
// v1 emit.AgentRole; value space identical so the engine can reuse the
// classification without a translation table.
type AgentRole string

const (
	RolePersona      AgentRole = "persona"
	RoleOrchestrator AgentRole = "orchestrator"
	RoleWorker       AgentRole = "worker"
	RoleSystem       AgentRole = "system"
)

// Severity is the 3-level enum chosen for v2.2 (vs v1's 4-level which
// included debug). Debug-level signals never go to wire — they belong in
// the engine log.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Status is the terminal status carried on card.close events. Recorded in
// payload.status (not envelope) because the same card may close multiple
// times in pathological cases (and the most recent close wins).
type Status string

const (
	StatusOK        Status = "ok"
	StatusFailed    Status = "failed"
	StatusSkipped   Status = "skipped"
	StatusCancelled Status = "cancelled"
)

// Channel identifies which content track a card.append chunk belongs to
// inside a card. Multiple channels can be open concurrently on one card
// (a message can stream text + tool_input at the same time).
type Channel string

const (
	ChannelText      Channel = "text"
	ChannelToolInput Channel = "tool_input"
	ChannelThinking  Channel = "thinking"
)

// TickKind identifies which kind of throttled / dropable signal a
// card.tick event carries.
type TickKind string

const (
	TickProgress   TickKind = "progress"
	TickHeartbeat  TickKind = "heartbeat"
	TickIntent     TickKind = "intent"
	TickNote       TickKind = "note"
	TickEscalation TickKind = "escalation"
)

// Hint is the UI rendering hint. Service MUST populate Title (registry
// templates handle the default case so business code can leave it empty).
// Renderer MAY override these on the client side.
type Hint struct {
	Title        string `json:"title"`
	Summary      string `json:"summary,omitempty"`
	Icon         string `json:"icon,omitempty"`
	InitialState string `json:"initial_state,omitempty"` // expanded | collapsed | hidden
	Persona      string `json:"persona,omitempty"`
}

// Metrics is filled on card.close events for billing / latency analysis.
type Metrics struct {
	DurationMs  int64    `json:"duration_ms,omitempty"`
	TokensIn    int      `json:"tokens_in,omitempty"`
	TokensOut   int      `json:"tokens_out,omitempty"`
	CacheRead   int      `json:"cache_read_tokens,omitempty"`
	CacheWrite  int      `json:"cache_write_tokens,omitempty"`
	CostUSD     float64  `json:"cost_usd,omitempty"`
	Model       string   `json:"model,omitempty"`
	BudgetSpent *Budget  `json:"budget_spent,omitempty"`
	BudgetLimit *Budget  `json:"budget_limit,omitempty"`
}

// Budget is the cost ceiling envelope used by both BudgetSpent and BudgetLimit.
type Budget struct {
	Tokens int     `json:"tokens,omitempty"`
	USD    float64 `json:"usd,omitempty"`
	Calls  int     `json:"calls,omitempty"`
}
