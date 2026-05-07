// Package wait defines the recovery primitives for the v2.2 channel
// protocol: pending prompts get persisted to disk before they reach the
// wire, so a server restart between emit and user-reply does not lose
// the conversation.
//
// The package is deliberately small and decoupled:
//
//   - types.go defines the data shapes (PendingWait, Anchor, Answer)
//     that flow between layers.
//   - Store is the persistence interface (concrete impl in
//     internal/storage/sqlite/waits.go).
//   - Resumer is the engine-facing interface used by the channel layer
//     when a user reply arrives for a wait that's no longer in memory
//     (i.e. recovery after restart).
//
// Lifecycle:
//
//	[A] Channel translator decides to emit prompt.user
//	[B] translator calls Store.Save(wait) — MUST succeed before emit
//	[C] translator emits prompt.user wire frame
//	[D] tool/coordinator goroutine blocks on live in-memory channel
//	    (or, if process restarts here, is GC'd; wait stays in SQLite)
//	[E] user replies prompt.user_response
//	[F] conn checks live in-memory channel first; on miss, checks
//	    Store.Get(reqID); on hit, calls Resumer.Resume(wait, answer)
//	[G] Resumer (engine impl) appends synthesised tool_result to
//	    session.messages and re-runs the query loop
//	[H] Store.Delete(reqID) — wait fully resolved
//
// At every step the data needed to recover is on disk before any
// volatile in-memory state is created.
package wait

import (
	"context"
	"time"
)

// Kind classifies the wait so the channel layer can route the user's
// answer back to the right engine subsystem.
type Kind string

const (
	KindQuestion   Kind = "question"     // AskUserQuestion tool
	KindPermission Kind = "permission"   // tool permission gate
	KindPlanReview Kind = "plan_review"  // PlanCoordinator user-confirmation
)

// PendingWait is one outstanding user prompt persisted to durable
// storage. The fields are an exhaustive set of what a clean restart
// needs to re-emit the prompt and to resume the engine after the user
// answers.
type PendingWait struct {
	// RequestID is the v2.2 wire-level request_id (e.g. "req_a1b2…").
	// Primary key — unique across all waits.
	RequestID string

	// SessionID is the session this wait belongs to. Index — channel
	// scans by session on reconnect to re-emit unanswered prompts.
	SessionID string

	// TraceID is the engine trace this wait was emitted in. Used for
	// observability; the engine's resume path may also use it to
	// correlate event sequences.
	TraceID string

	// Kind discriminates the wait type. The channel uses Kind to know
	// what shape of Answer to expect from the user's prompt.user_response
	// payload, and to call the appropriate Resumer code path.
	Kind Kind

	// CorrelationID is the engine-side identifier the wait correlates
	// with: tool_use_id for KindQuestion, perm_xxx for KindPermission,
	// plan_id for KindPlanReview. Engine.Resume uses this to thread the
	// answer back into engine state.
	CorrelationID string

	// PromptFrame is the original prompt.user wire frame as bytes,
	// stored so that on reconnect the channel can re-emit it byte-for-
	// byte to a new connection (useful when the client UI lost its
	// state and doesn't remember which prompt was open).
	PromptFrame []byte

	// Anchor captures where in the engine the wait was created —
	// enough info for Resumer to decide whether/how to resume.
	Anchor Anchor

	// CreatedAt is when the wait was first persisted. TTL cleanup uses
	// this; observability uses it to flag stale waits.
	CreatedAt time.Time

	// ExpiresAt is the soft deadline. After this point the wait may be
	// cleaned up by the periodic sweeper. Zero value = no expiry.
	ExpiresAt time.Time
}

// Anchor captures the "where" of a wait so the resume path can
// reconstruct enough engine context to re-enter the query loop. Kept
// minimal on purpose: Phase 1 only supports top-level (emma) waits, but
// the schema is forward-compatible with Phase 2 (sub-agent waits) and
// Phase 3 (full coordinator state) without requiring a DB migration.
type Anchor struct {
	// MessageIndex is the length of session.messages at the moment the
	// wait was created. The Resumer uses this to know exactly where in
	// the conversation to insert the synthesised tool_result.
	MessageIndex int

	// AgentPath identifies the agent stack at the wait point. "main" =
	// emma (top level). "main/sub_e5" = sub-agent that emma spawned.
	// Phase 1 only resumes when AgentPath == "main"; deeper paths emit
	// turn_aborted on resolution.
	AgentPath string

	// CoordinatorMode echoes the active L2 coordinator at wait time.
	// Empty for plain ReAct turns; "plan" when inside a PlanCoordinator
	// run. Used by Resumer to decide whether to re-enter via the same
	// coordinator on resume.
	CoordinatorMode string
}

// Answer is the user's reply to a prompt, normalised across the three
// kinds. The channel layer parses prompt.user_response into this shape
// before handing it off to the Resumer.
type Answer struct {
	// Decision is "approved" or "denied". For KindQuestion an answer is
	// always treated as approved; denied means the user cancelled.
	Decision string

	// Output is the user's textual answer (KindQuestion's selected
	// options joined, KindPermission's optional message, KindPlanReview's
	// optional reason).
	Output string

	// Edits is kind-specific structured data. For KindPlanReview this
	// carries the user's updated_steps. For other kinds it's empty.
	Edits map[string]any
}

// Store is the durable persistence interface. Implementations go in
// internal/storage/* (sqlite is the production impl; tests can use a
// memory impl).
//
// All methods are safe for concurrent use.
type Store interface {
	// Save persists a wait. Returns an error if write fails — callers
	// MUST NOT proceed to emit the wire frame on error (the wait would
	// be unrecoverable on restart).
	Save(ctx context.Context, w PendingWait) error

	// Get retrieves a wait by its request_id. Returns (nil, nil) when
	// the wait does not exist (not an error).
	Get(ctx context.Context, requestID string) (*PendingWait, error)

	// Delete removes a wait. No-op when the wait doesn't exist; safe to
	// call multiple times for the same id.
	Delete(ctx context.Context, requestID string) error

	// ListBySession returns all unresolved waits for a session. Used at
	// reconnect to re-emit unanswered prompts to the new connection.
	// Returns an empty slice (never nil) on no-match.
	ListBySession(ctx context.Context, sessionID string) ([]*PendingWait, error)

	// DeleteExpired removes waits whose ExpiresAt is before now. Returns
	// the number deleted. Called periodically by Manager to bound table
	// growth. Waits with zero ExpiresAt are never expired.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// Resumer is implemented by the engine. The channel layer calls
// Resumer.Resume when a prompt.user_response arrives for a wait that's
// no longer live in memory (i.e. server restarted between emit and
// reply, OR the original goroutine's ctx was cancelled and replaced).
//
// The implementation must be idempotent: a second Resume call for the
// same RequestID after the wait has been resolved should be a safe
// no-op. The channel deletes the wait from Store on successful Resume,
// but a duplicate inflight call is possible.
type Resumer interface {
	Resume(ctx context.Context, wait *PendingWait, answer Answer) error
}

// ResumerFunc adapts a function to the Resumer interface.
type ResumerFunc func(ctx context.Context, w *PendingWait, a Answer) error

// Resume implements Resumer.
func (f ResumerFunc) Resume(ctx context.Context, w *PendingWait, a Answer) error {
	return f(ctx, w, a)
}

// DefaultExpiry is the recommended ExpiresAt offset for new waits.
// 15 days is long enough that a user who walks away for a vacation
// or is blocked on external information can still come back and
// answer; the periodic sweeper still bounds table size on the order
// of "longest abandoned conversation in the last fortnight".
const DefaultExpiry = 15 * 24 * time.Hour
