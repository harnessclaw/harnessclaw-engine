// Package channel defines the adapter contracts between the engine and
// the outside world: chat clients (WebSocket / Feishu / WeChat), HTTP API
// endpoints, broadcast notifiers (PagerDuty / DingTalk alert), etc.
//
// Capabilities are split by responsibility and composed:
//
//	Channel  — base metadata + lifecycle
//	Inbound  — incoming side: Messages() <-chan *IncomingMessage
//	Replier  — reply side:    Reply(ctx, sessionID, Outbound)
//	Notifier — proactive push (session-independent alerts / notifications)
//	Duplex   — Inbound + Replier, the typical conversational adapter
package channel

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// Channel is the base metadata + lifecycle shared by every adapter.
//
// Start is non-blocking: it returns immediately and runs background
// goroutines (accept connections / pull messages). Close performs a
// graceful shutdown (stop the server, drain connections, close the
// channel returned by Messages()).
type Channel interface {
	Name() string
	Start(ctx context.Context) error
	Close() error
	Health() error
}

// Inbound is the incoming side: messages flowing in from the outside.
//
// Messages returns a read-only channel that is closed by Close();
// consumers iterate with for-range. The element type stays
// pkg/types.IncomingMessage so the rich fields (ToolResult /
// PermissionResponse / PlanResponse / StepDecisionResponse / Content /
// CoordinatorMode etc.) are preserved end-to-end.
type Inbound interface {
	Channel
	Messages() <-chan *types.IncomingMessage
}

// Replier is the reply side: write engine output back to the session
// that produced the inbound message.
//
// Lifecycle contract: the caller owns msg.Stream and must close it.
// Reply blocks until Stream is drained, then processes Final (which may
// be nil). Stream and Final can be supplied independently or together.
type Replier interface {
	Channel
	Reply(ctx context.Context, sessionID string, msg Outbound) error
}

// Notifier is the proactive-push capability: session-independent
// alerts / notifications initiated by an agent.
//
// Typical use cases: long-running task completed, alert condition
// tripped, scheduled health check failed. The agent ships the message
// through a Notifier to an ops channel (Feishu group, WeCom, PagerDuty).
type Notifier interface {
	Channel
	Notify(ctx context.Context, target Target, msg Notification) error
}

// Duplex is the bidirectional conversational adapter (WebSocket / HTTP
// / Feishu Bot etc.).
type Duplex interface {
	Inbound
	Replier
}

// ─── Data model ───

// Outbound describes the payload Replier.Reply sends out.
//
//   - Streaming only (the typical LLM output): set Stream; closing it
//     marks the end.
//   - Final only (batched result notification): set Final, leave Stream nil.
//   - Both: stream events first, then finalize with Final (useful for
//     locking down a card after the stream).
type Outbound struct {
	// Stream matches the <-chan types.EngineEvent that engine.ProcessMessage
	// returns. A value channel (rather than pointer) is used so the router
	// can forward the engine stream verbatim without a per-event copy step.
	Stream <-chan types.EngineEvent
	Final  *types.Message
}

// Target identifies a Notify destination. The (Type, ID) pair is
// interpreted by the concrete Notifier.
type Target struct {
	Type string // "user" / "group" / "channel"
	ID   string // platform-internal address (Feishu open_id / group chat_id / ...)
}

// Notification is one proactive-push payload.
type Notification struct {
	// Source identifies the sender (agent_id / task_id / ...) for ops
	// triage. Optional.
	Source string

	Title    string
	Content  string
	Priority Priority

	// Action carries an optional callback button so the rendered card
	// can be clicked through.
	Action *Action
}

// Priority is the notification priority. Each Notifier maps it to its
// platform's alert level.
type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
	PriorityUrgent
)

// Action describes a single callback button on a notification card.
type Action struct {
	Label string
	URL   string
}
