package emitv2

import (
	"encoding/json"
	"sync"
)

// Sink is the destination an Emitter writes events to. The WS channel
// implementation, the in-memory test recorder, and the (future) OTel
// exporter all implement Sink.
type Sink interface {
	// Send delivers one event. Sinks are responsible for thread-safety.
	// A Sink MAY drop events with Type == EventCardTick (telemetry) under
	// backpressure, but MUST NOT drop EventCardAppend (stream) or any
	// State / Interaction / Lifecycle event.
	Send(Event)
}

// SinkFunc adapts a plain function to the Sink interface.
type SinkFunc func(Event)

// Send implements Sink.
func (f SinkFunc) Send(e Event) { f(e) }

// MultiSink fans an event out to multiple sinks. Useful for "WS + log"
// or "WS + OTLP" deployments.
type MultiSink struct {
	sinks []Sink
}

// NewMultiSink constructs a MultiSink fanning to all provided sinks.
func NewMultiSink(sinks ...Sink) *MultiSink {
	return &MultiSink{sinks: sinks}
}

// Send dispatches to every child sink in order.
func (m *MultiSink) Send(e Event) {
	for _, s := range m.sinks {
		s.Send(e)
	}
}

// RecorderSink captures events into a slice. Used by tests and replay
// tooling. Goroutine-safe.
type RecorderSink struct {
	mu     sync.Mutex
	events []Event
}

// NewRecorder constructs an empty RecorderSink.
func NewRecorder() *RecorderSink {
	return &RecorderSink{}
}

// Send appends e to the recorded slice.
func (r *RecorderSink) Send(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Events returns a copy of the recorded events. Safe to call concurrently
// with Send.
func (r *RecorderSink) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Reset clears the recorded events.
func (r *RecorderSink) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// JSONLines marshals all recorded events as newline-delimited JSON.
// Useful for golden-file tests and debugging.
func (r *RecorderSink) JSONLines() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var buf []byte
	for _, e := range r.events {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	return buf, nil
}

// FilterByType returns events matching the given type. Convenience for
// tests that want to assert "exactly N card.close events fired".
func (r *RecorderSink) FilterByType(t EventType) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// FilterByCard returns events for a specific card_id.
func (r *RecorderSink) FilterByCard(cardID string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Envelope.CardID == cardID {
			out = append(out, e)
		}
	}
	return out
}
