package userprompt

import (
	"sync"

	"harnessclaw-go/internal/engine/wait"
)

// liveRegistry holds the in-memory map of "the engine goroutine that
// emitted prompt X is blocked waiting on this channel". When a user's
// answer arrives in real time (no restart), Deliver finds the channel
// here and unblocks the engine.
//
// Goroutine-safe.
type liveRegistry struct {
	mu  sync.Mutex
	chs map[string]chan wait.Answer
}

func newLiveRegistry() *liveRegistry {
	return &liveRegistry{chs: make(map[string]chan wait.Answer)}
}

// register creates a buffered channel (capacity 1, so deliver never
// blocks) and stores it under requestID. The returned receive-only
// view is what the engine's blocking goroutine selects on.
func (r *liveRegistry) register(requestID string) <-chan wait.Answer {
	ch := make(chan wait.Answer, 1)
	r.mu.Lock()
	r.chs[requestID] = ch
	r.mu.Unlock()
	return ch
}

// unregister removes the entry. Called from the issuer's defer so the
// registry doesn't leak entries when the engine goroutine exits
// (success, ctx-cancel, or panic).
func (r *liveRegistry) unregister(requestID string) {
	r.mu.Lock()
	delete(r.chs, requestID)
	r.mu.Unlock()
}

// deliver pushes the answer onto the channel. Returns false when no
// channel is registered for requestID (engine goroutine is gone —
// caller must fall through to the persisted-wait recovery path).
//
// Non-blocking: capacity-1 buffer means the engine's select unblocks
// on the very next scheduler tick. If the channel is somehow full
// (would only happen on a duplicate Deliver for the same id) the
// extra answer is silently discarded — the engine already has its
// answer.
func (r *liveRegistry) deliver(requestID string, ans wait.Answer) bool {
	r.mu.Lock()
	ch, ok := r.chs[requestID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- ans:
	default:
	}
	return true
}
