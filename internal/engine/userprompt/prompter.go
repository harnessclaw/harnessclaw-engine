// Package prompter is the recovery-aware glue between the channel
// (which talks to the user) and the engine (which is the thing waiting).
//
// Two responsibilities:
//
//  1. WHEN A WAIT BEGINS: persist it durably (wait.Store) BEFORE any
//     wire frame goes out. If the persist fails, the prompt is never
//     emitted — the user can never see and answer something the server
//     can't recover.
//
//  2. WHEN AN ANSWER ARRIVES: route to the live in-memory waiter if one
//     exists (the common case); otherwise the channel falls through to
//     the recovery path (Resumer) using the persisted wait.
//
// Tool authors and the engine's tool executor are NOT direct clients of
// Prompter. The channel layer (translator + conn) wires Prompter into
// the prompt.user / prompt.user_response flow. Tool code stays focused
// on its work — recovery is fully transparent.
package userprompt

import (
	"context"
	"fmt"
	"time"

	"harnessclaw-go/internal/engine/wait"
)

// Prompter is the channel-side service. It owns the live wait registry
// (in-memory) and delegates persistent storage to wait.Store.
//
// Goroutine-safe: all methods may be called concurrently.
type Prompter struct {
	store wait.Store
	live  *liveRegistry

	now func() time.Time // injectable for tests
}

// Config is the constructor input.
type Config struct {
	Store wait.Store
	Now   func() time.Time // optional, defaults to time.Now
}

// New constructs a Prompter. Store is required.
func New(cfg Config) *Prompter {
	if cfg.Store == nil {
		panic("prompter.New: Store required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Prompter{
		store: cfg.Store,
		live:  newLiveRegistry(),
		now:   cfg.Now,
	}
}

// Issue is called by the channel translator at the moment it decides to
// emit a prompt.user frame. Persists the wait FIRST, then registers an
// in-memory channel for the live answer path.
//
// Order matters: SaveWait must succeed before the wire frame goes out.
// If SaveWait fails, the caller MUST NOT emit — the answer would be
// unrecoverable.
//
// Returns the live channel the engine's blocking goroutine selects on,
// plus a teardown function the caller defers to clean up the registry
// entry on completion (success OR failure).
//
// Typical usage:
//
//	answerCh, done, err := prompter.Issue(ctx, w)
//	if err != nil { return err }
//	defer done()
//	em.PromptUserWithID(w.RequestID, ...)  // safe to emit now
//	select {
//	case ans := <-answerCh: ...            // live path
//	case <-ctx.Done():                     // cancellation / shutdown
//	}
func (p *Prompter) Issue(ctx context.Context, w wait.PendingWait) (<-chan wait.Answer, func(), error) {
	if w.CreatedAt.IsZero() {
		w.CreatedAt = p.now()
	}
	if w.ExpiresAt.IsZero() {
		w.ExpiresAt = w.CreatedAt.Add(wait.DefaultExpiry)
	}
	if err := p.store.Save(ctx, w); err != nil {
		return nil, func() {}, fmt.Errorf("persist wait %s: %w", w.RequestID, err)
	}
	ch := p.live.register(w.RequestID)
	done := func() { p.live.unregister(w.RequestID) }
	return ch, done, nil
}

// Deliver routes an answer to the live waiter for requestID. Returns
// true when a live waiter received the answer; false when no live
// waiter is registered (i.e. the engine that issued the wait is gone —
// caller should fall through to the recovery path via Resume).
//
// Non-blocking: if the live waiter's channel buffer is full (it
// shouldn't ever be, capacity is 1) the answer is silently dropped to
// avoid producer blocking.
func (p *Prompter) Deliver(requestID string, answer wait.Answer) bool {
	return p.live.deliver(requestID, answer)
}

// Forget removes the persisted wait once an answer has been fully
// processed by the engine. Call after Resume succeeds, or after the
// live path's blocking goroutine has consumed the answer and returned.
func (p *Prompter) Forget(ctx context.Context, requestID string) error {
	return p.store.Delete(ctx, requestID)
}

// IssueWait persists a wait without registering an in-memory live
// waiter. Used by the channel translator on the emit-side (the LIVE
// waiter is registered separately, by the engine when it actually
// blocks — for client-routed tools the engine's tool executor owns
// that). Equivalent to Issue minus the live registration.
func (p *Prompter) IssueWait(ctx context.Context, w wait.PendingWait) error {
	if w.CreatedAt.IsZero() {
		w.CreatedAt = p.now()
	}
	if w.ExpiresAt.IsZero() {
		w.ExpiresAt = w.CreatedAt.Add(wait.DefaultExpiry)
	}
	if err := p.store.Save(ctx, w); err != nil {
		return fmt.Errorf("persist wait %s: %w", w.RequestID, err)
	}
	return nil
}

// Lookup retrieves a persisted wait. The channel calls this on
// prompt.user_response when the in-memory live registry doesn't know
// the request_id — i.e. the server restarted after Issue and before
// Deliver, or the engine's goroutine was cancelled.
func (p *Prompter) Lookup(ctx context.Context, requestID string) (*wait.PendingWait, error) {
	return p.store.Get(ctx, requestID)
}

// ListSession returns all unanswered waits for a session. Used at
// reconnect to re-emit unresolved prompts so the client UI can re-
// render whatever it lost.
func (p *Prompter) ListSession(ctx context.Context, sessionID string) ([]*wait.PendingWait, error) {
	return p.store.ListBySession(ctx, sessionID)
}

// SweepExpired deletes waits past their expiry. The Manager calls this
// periodically (e.g. every minute) to bound table growth.
func (p *Prompter) SweepExpired(ctx context.Context) (int, error) {
	return p.store.DeleteExpired(ctx, p.now())
}
