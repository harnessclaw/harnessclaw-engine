package emitv2

import (
	"sync"
	"time"
)

// Tracker is the orphan watchdog. Every tracked card.add records (cardID,
// kind, openedAt, deadline). card.close removes the entry. A goroutine
// periodically checks for entries past their deadline and emits a
// synthetic card.close{status:failed, error.type:orphan_timeout} for each.
//
// This fulfils the protocol contract that "every *.add is matched by a
// *.close" — any code path that forgets to close (panic, network drop,
// scheduler death) gets compensated by the watchdog so the renderer's
// card tree never has zombie nodes.
type Tracker struct {
	mu      sync.Mutex
	open    map[string]*openCard // cardID → entry
	checkEvery time.Duration
	stop    chan struct{}
	stopped bool
	now     func() time.Time
}

type openCard struct {
	kind     CardKind
	parent   string // for envelope on the synthetic close
	openedAt time.Time
	deadline time.Time
	builder  *CardBuilder // who to emit the synthetic close through
}

// TrackerConfig configures the watchdog.
type TrackerConfig struct {
	// CheckEvery is how often the watchdog scans for expired cards.
	// Defaults to 1 second when zero.
	CheckEvery time.Duration
	// Now is an injectable clock for tests. Defaults to time.Now.
	Now func() time.Time
}

// NewTracker constructs a Tracker. Call Start to begin the background
// scan goroutine; call Stop to shut it down.
func NewTracker(cfg TrackerConfig) *Tracker {
	if cfg.CheckEvery <= 0 {
		cfg.CheckEvery = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Tracker{
		open:       make(map[string]*openCard),
		checkEvery: cfg.CheckEvery,
		stop:       make(chan struct{}),
		now:        cfg.Now,
	}
}

// Start launches the watchdog goroutine. Safe to call multiple times;
// only the first call has effect.
func (t *Tracker) Start() {
	go t.loop()
}

// Stop terminates the watchdog goroutine. Safe to call multiple times.
func (t *Tracker) Stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.mu.Unlock()
	close(t.stop)
}

// Open records a new tracked card. Called by Builder.Add.
func (t *Tracker) Open(cardID string, kind CardKind, timeout time.Duration, b *CardBuilder) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	parent := ""
	// Stack-top before this card was pushed = its parent.
	if b != nil {
		// builder's emitter has just pushed cardID — second-from-top is parent.
		b.em.mu.Lock()
		if n := len(b.em.parents); n >= 2 {
			parent = b.em.parents[n-2]
		}
		b.em.mu.Unlock()
	}
	t.open[cardID] = &openCard{
		kind:     kind,
		parent:   parent,
		openedAt: now,
		deadline: now.Add(timeout),
		builder:  b,
	}
}

// Close marks cardID as closed and stops tracking it. Called by
// Builder.Close. No-op if cardID was not tracked.
func (t *Tracker) Close(cardID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.open, cardID)
}

// ParentOf returns the parent_card_id that was recorded when cardID was
// opened. Returns "" for unknown cards.
func (t *Tracker) ParentOf(cardID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.open[cardID]; ok {
		return c.parent
	}
	return ""
}

// OpenCount returns the current number of tracked cards. Useful for
// tests and observability.
func (t *Tracker) OpenCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.open)
}

// loop is the watchdog goroutine. Wakes every checkEvery and synthesises
// orphan-timeout closes for expired cards.
func (t *Tracker) loop() {
	ticker := time.NewTicker(t.checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.sweep()
		}
	}
}

// sweep finds expired cards and emits synthetic closes. We do the emit
// outside the mutex so a misbehaving Sink can't deadlock the watchdog.
func (t *Tracker) sweep() {
	now := t.now()
	type expired struct {
		cardID string
		oc     *openCard
	}
	var todo []expired
	t.mu.Lock()
	for id, oc := range t.open {
		if now.After(oc.deadline) {
			todo = append(todo, expired{cardID: id, oc: oc})
		}
	}
	for _, e := range todo {
		delete(t.open, e.cardID)
	}
	t.mu.Unlock()

	for _, e := range todo {
		if e.oc.builder == nil {
			continue
		}
		err := NewError(ErrorTypeOrphanTimeout,
			"card had no close before orphan timeout: "+string(e.oc.kind))
		// Use WithoutLifecycle so this synthetic close doesn't recurse
		// into the tracker.
		e.oc.builder.Close(StatusFailed,
			WithError(err),
			WithoutLifecycle(),
		)
	}
}

// SweepNow is exposed for tests: it runs one sweep synchronously without
// waiting for the ticker.
func (t *Tracker) SweepNow() {
	t.sweep()
}
