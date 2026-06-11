// Package emit defines the structured event envelope, display, and metrics
// types used by the harnessclaw engine to broadcast lifecycle events to
// observers (WebSocket clients, monitoring, audit log).
//
// Design reference: docs/protocols/websocket.md §6.13.
//
// Emit is framework-triggered, not LLM-triggered: the engine layer decides
// when to emit and what envelope/severity to attach. LLM output only
// contributes to the Display fields (title, summary) when configured.
//
// Versioning: emit follows the WebSocket protocol_version. There is no
// separate schema_version — clients gate on protocol_version alone.
package emit

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// AgentRole classifies the responsibility of the agent that emitted the
// event. The role enum is deliberately responsibility-oriented rather
// than tied to a specific architectural layer (L1/L2/L3) so the protocol
// survives internal refactors. Mapping today:
//   - persona      → user-facing chat agent (emma)
//   - orchestrator → planner / coordinator that dispatches work
//   - worker       → sub-agent executing tools to do real work
//   - system       → framework-level events not tied to a specific run
type AgentRole string

const (
	RolePersona      AgentRole = "persona"
	RoleOrchestrator AgentRole = "orchestrator"
	RoleWorker       AgentRole = "worker"
	RoleSystem       AgentRole = "system"
)

// Severity classifies event log level.
type Severity string

const (
	SeverityDebug Severity = "debug"
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Visibility controls how the event surfaces in the activity panel.
type Visibility string

const (
	VisibilityDefault   Visibility = "default"   // expanded by default
	VisibilityCollapsed Visibility = "collapsed" // collapsed by default; user can expand
	VisibilityHidden    Visibility = "hidden"    // log-only, never shown to the user
)

// Icon is the controlled set of icon identifiers a Display block may
// reference. The wire representation is a string; producers MAY emit
// values outside this enum, but clients MUST fall back to IconDefault
// when they do not recognise a value. New icons are added in MINOR
// protocol releases; removing an icon is a breaking change.
type Icon string

const (
	IconPlan     Icon = "plan"
	IconDispatch Icon = "dispatch"
	IconSearch   Icon = "search"
	IconAnalysis Icon = "analysis"
	IconTool     Icon = "tool"
	IconSuccess  Icon = "success"
	IconError    Icon = "error"
	IconWarning  Icon = "warning"
	IconInfo     Icon = "info"
	IconAgent    Icon = "agent"
	IconTask     Icon = "task"
	IconStep     Icon = "step"
	IconDefault  Icon = "default"
)

// Envelope is the metadata wrapper present on every emitted event. The
// framework fills it; the LLM never writes to it directly.
//
// All event consumers can route on envelope fields without parsing the
// payload — a key design principle of the emit protocol.
//
// NOTE: emit events still carry the message-level top-level fields
// (`event_id`, `session_id`) defined in §4 Message Format. The envelope
// is additive and must not duplicate them — the wire `event_id` IS the
// envelope event id; there is no separate field. See §6.13.1.
type Envelope struct {
	// EventID is the globally-unique identifier for this event. UUIDv4
	// by default; the runtime allocates one per Emit call. This is the
	// SAME id that appears as the message-level top-level event_id.
	EventID string `json:"event_id"`
	// TraceID groups every event produced by a single user request.
	// Stable for the lifetime of one user-input → assistant-reply round.
	TraceID string `json:"trace_id"`
	// ParentEventID links this event to the immediate causal parent
	// (e.g. step.started → plan.created). Empty when the event is the
	// root of its trace.
	ParentEventID string `json:"parent_event_id,omitempty"`
	// TaskID identifies the task node this event belongs to. Multiple
	// events share a task_id so the UI can aggregate them onto one card.
	TaskID string `json:"task_id,omitempty"`
	// ParentTaskID describes the task hierarchy (plan → step).
	ParentTaskID string `json:"parent_task_id,omitempty"`
	// Seq is a per-trace monotonic sequence number. Lets the client sort
	// events deterministically when network reordering happens.
	Seq int64 `json:"seq"`
	// Timestamp is the moment the event was emitted (UTC, ms precision).
	Timestamp time.Time `json:"timestamp"`
	// AgentRole classifies the producer's responsibility (persona /
	// orchestrator / worker / system). NOT a positional layer label —
	// see AgentRole doc for the rationale.
	AgentRole AgentRole `json:"agent_role"`
	// AgentID identifies the specific agent instance. Empty for system events.
	AgentID string `json:"agent_id,omitempty"`
	// AgentRunID identifies one execution of the agent (an agent may run
	// multiple times within a single trace).
	AgentRunID string `json:"agent_run_id,omitempty"`
	// Severity is the log level for monitoring/filtering purposes.
	Severity Severity `json:"severity"`
}

// Display carries the user-friendly rendering hints. The client renders
// from Display alone — it never has to parse the payload to draw a card.
//
// Producers should provide at minimum a Title; Summary, Icon, Visibility
// are optional refinements.
type Display struct {
	Title       string     `json:"title,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Icon        Icon       `json:"icon,omitempty"`
	Visibility  Visibility `json:"visibility,omitempty"`
	PersonaHint string     `json:"persona_hint,omitempty"`
}

// Metrics carries cost and performance data. Filled on terminal events
// (*.finished, *.completed, *.failed) so observers can do cost attribution
// and latency analysis. Empty on non-terminal events.
type Metrics struct {
	DurationMs int64   `json:"duration_ms,omitempty"`
	TokensIn   int     `json:"tokens_in,omitempty"`
	TokensOut  int     `json:"tokens_out,omitempty"`
	CacheRead  int     `json:"cache_read_tokens,omitempty"`
	CacheWrite int     `json:"cache_write_tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Model      string  `json:"model,omitempty"`
}

// Sequencer dispenses monotonically-increasing sequence numbers per trace.
// Backed by sync.Map so concurrent traces don't contend on a global mutex.
type Sequencer struct {
	traces sync.Map // traceID → *atomic.Int64
}

// NewSequencer constructs an empty Sequencer.
func NewSequencer() *Sequencer {
	return &Sequencer{}
}

// Next returns the next seq number for the given trace. The first call
// for a fresh trace returns 1.
func (s *Sequencer) Next(traceID string) int64 {
	v, _ := s.traces.LoadOrStore(traceID, new(atomic.Int64))
	counter := v.(*atomic.Int64)
	return counter.Add(1)
}

// Drop releases the per-trace counter. Call when a trace finishes so
// memory does not grow unbounded on a long-running server.
func (s *Sequencer) Drop(traceID string) {
	s.traces.Delete(traceID)
}

// NewTraceID allocates a new trace identifier. The "tr_" prefix mirrors
// the convention in the protocol design doc.
func NewTraceID() string {
	return "tr_" + strings.Replace(uuid.NewString(), "-", "", -1)[:24]
}

// NewEventID allocates a new event identifier. Event IDs are UUID-based
// for global uniqueness; we don't depend on UUIDv7 ordering because the
// envelope already carries Seq for ordering.
func NewEventID() string {
	return "evt_" + strings.Replace(uuid.NewString(), "-", "", -1)[:20]
}

// NewAgentRunID allocates a new agent-run identifier.
func NewAgentRunID() string {
	return "run_" + strings.Replace(uuid.NewString(), "-", "", -1)[:16]
}

// FormatDuration is a small helper for callers that need to render a
// duration into Display.Summary text.
func FormatDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(float64(ms)/1000.0, 'f', 1, 64) + "s"
}

// ErrorType is the controlled enum of error categories shared between
// the §6.12 connection-level `error` event and the §6.13 emit lifecycle
// failure events (trace.failed / plan.failed / step.failed). Keeping
// one taxonomy across both lets monitoring rules apply uniformly.
//
// Producers MUST pick the closest matching value; clients MUST treat
// unknown values as ErrorTypeInternal and fall back gracefully.
type ErrorType string

const (
	// Connection / API level (mirrors §6.12)
	ErrorTypeAuthentication  ErrorType = "authentication_error"
	ErrorTypePermission      ErrorType = "permission_error"
	ErrorTypeNotFound        ErrorType = "not_found_error"
	ErrorTypeRateLimit       ErrorType = "rate_limit_error"
	ErrorTypeInvalidRequest  ErrorType = "invalid_request_error"
	ErrorTypeOverloaded      ErrorType = "overloaded_error"
	ErrorTypeInternal        ErrorType = "internal_error"

	// Tool execution
	ErrorTypeToolTimeout      ErrorType = "tool_timeout"
	ErrorTypeToolRateLimited  ErrorType = "tool_rate_limited"
	ErrorTypeToolInvalidInput ErrorType = "tool_invalid_input"

	// LLM
	ErrorTypeLLMTimeout       ErrorType = "llm_timeout"
	ErrorTypeLLMContentFilter ErrorType = "llm_content_filter"

	// Agent / orchestration
	ErrorTypeAgentMaxTurns  ErrorType = "agent_max_turns_exceeded"
	ErrorTypeDependencyFail ErrorType = "dependency_failed"
	ErrorTypeOrphanTimeout  ErrorType = "orphan_timeout"
	ErrorTypeAborted        ErrorType = "aborted"
)

// ErrorCode is a free-form machine-readable code that further qualifies
// the ErrorType. For example ErrorTypeToolTimeout might use the code
// "BASH_TIMEOUT" or "WEBFETCH_TIMEOUT". Codes are NOT enumerated by
// the protocol — they're scoped to the producing tool.
type ErrorCode = string

// ErrorInfo is the canonical error block used by both §6.12 connection
// errors and §6.13 emit failure payloads. Producers MUST set Type and
// Message; UserMessage is the persona-friendly fallback L1 quotes back
// to the user (so the user never sees raw stack traces or error codes).
type ErrorInfo struct {
	Type        ErrorType `json:"type"`
	Code        string    `json:"code,omitempty"`
	Message     string    `json:"message"`
	UserMessage string    `json:"user_message,omitempty"`
	Retryable   bool      `json:"retryable,omitempty"`
}
