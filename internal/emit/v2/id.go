package emitv2

import (
	"strings"

	"github.com/google/uuid"
)

// ID generators. UI-first: short prefixed identifiers that read well in
// debug logs and chat transcripts, not OTel-format hex strings. If we
// ever export to OpenTelemetry, the exporter will derive 16/8-byte hex
// IDs by hashing — see appendix A of v2.2 protocol doc.

// NewTraceID allocates a new trace identifier. One per user-input → reply.
func NewTraceID() string {
	return "tr_" + strings.Replace(uuid.NewString(), "-", "", -1)[:24]
}

// NewEventID allocates a new event identifier (one per emit call).
func NewEventID() string {
	return "evt_" + strings.Replace(uuid.NewString(), "-", "", -1)[:20]
}

// NewCardID allocates a new card identifier. Caller usually passes a
// pre-allocated ID (so the same card_id can appear on add → set → close
// without coordination); use this helper when you don't have one.
func NewCardID(kind CardKind) string {
	prefix := "card_"
	switch kind {
	case CardTurn:
		prefix = "turn_"
	case CardMessage:
		prefix = "msg_"
	case CardTool:
		prefix = "tool_"
	case CardAgent:
		prefix = "ag_"
	case CardPlan:
		prefix = "plan_"
	case CardStep:
		prefix = "step_"
	case CardArtifact:
		prefix = "art_"
	case CardThinking:
		prefix = "thk_"
	case CardTodo:
		prefix = "todo_"
	}
	return prefix + strings.Replace(uuid.NewString(), "-", "", -1)[:16]
}

// NewAgentRunID allocates a new agent-run identifier (one per spawn).
func NewAgentRunID() string {
	return "run_" + strings.Replace(uuid.NewString(), "-", "", -1)[:16]
}

// NewRequestID allocates a new request identifier for prompt.user / reply.
func NewRequestID() string {
	return "req_" + strings.Replace(uuid.NewString(), "-", "", -1)[:16]
}
