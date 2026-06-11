package emitv2

import (
	"sync"
	"sync/atomic"
)

// Sequencer dispenses monotonically-increasing per-trace seq numbers.
// Every event in a trace gets a unique, ordered Seq; clients use it to
// reconcile event order after reconnect or when worker pools fan out.
//
// Thread-safe: safe to call Next from multiple goroutines.
type Sequencer struct {
	traces sync.Map // traceID → *atomic.Int64
}

// NewSequencer constructs an empty Sequencer.
func NewSequencer() *Sequencer {
	return &Sequencer{}
}

// Next returns the next seq number for traceID. The first call for a
// fresh trace returns 1.
func (s *Sequencer) Next(traceID string) int64 {
	v, _ := s.traces.LoadOrStore(traceID, new(atomic.Int64))
	return v.(*atomic.Int64).Add(1)
}

// Drop releases the per-trace counter. Call when a trace finishes so
// memory does not grow unbounded on long-running servers.
func (s *Sequencer) Drop(traceID string) {
	s.traces.Delete(traceID)
}

// Peek returns the most recent seq number issued for traceID without
// allocating a new one. Useful for tests / observability. Returns 0 if
// no events have been issued for traceID.
func (s *Sequencer) Peek(traceID string) int64 {
	v, ok := s.traces.Load(traceID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}
