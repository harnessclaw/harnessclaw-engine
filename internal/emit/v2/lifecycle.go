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
	mu         sync.Mutex
	open       map[string]*openCard // cardID → entry for cards currently open
	parentOf   map[string]string    // cardID → parent_card_id; PERMANENT — survives Close so Touch can still walk up through already-closed ancestors. Cleared only when the Tracker itself shuts down (one tracker per session).
	checkEvery time.Duration
	stop       chan struct{}
	stopped    bool
	now        func() time.Time
}

type openCard struct {
	kind     CardKind
	parent   string // for envelope on the synthetic close
	openedAt time.Time
	deadline time.Time
	timeout  time.Duration // original timeout, used to recompute deadline on Resume
	paused   bool          // true while a prompt.user awaiting user response keeps this card alive
	builder  *CardBuilder  // who to emit the synthetic close through
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
		parentOf:   make(map[string]string),
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

// Open records a new tracked card. Called by Builder.Add. parent is the
// explicit parent_card_id the card was opened under (typically the
// envelope.parent_card_id the wire envelope just carried). Empty parent
// means "root card". The Tracker uses this for the heartbeat chain
// (Touch / SuspendChain / ResumeChain) — getting it right is what
// keeps the watchdog matching the on-wire parent topology when callers
// override with WithParent (e.g. plan-mode sub-agent rooting under a
// step card emitted from the main emitter).
func (t *Tracker) Open(cardID string, kind CardKind, timeout time.Duration, parent string, b *CardBuilder) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.open[cardID] = &openCard{
		kind:     kind,
		parent:   parent,
		openedAt: now,
		deadline: now.Add(timeout),
		timeout:  timeout,
		builder:  b,
	}
	// Record the parent link permanently. After this card closes, Touch
	// on a still-open descendant must be able to skip over this card and
	// keep refreshing the deadline of further ancestors — otherwise a
	// long-running grandchild orphan-times the still-relevant root card
	// (the bug that killed turn cards while scheduler was still
	// running underneath an already-closed message card).
	t.parentOf[cardID] = parent
}

// Close marks cardID as closed and stops tracking it. Called by
// Builder.Close. No-op if cardID was not tracked.
func (t *Tracker) Close(cardID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.open, cardID)
}

// ParentOf returns the parent_card_id that was recorded when cardID was
// opened. Survives Close — callers that need to know "what was the
// parent of this (now possibly closed) card" get a stable answer for the
// lifetime of the Tracker.
func (t *Tracker) ParentOf(cardID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parentOf[cardID]
}

// Suspend pauses the orphan watchdog for cardID. While paused, the card
// is kept alive — sweep skips it and no synthetic close fires no matter
// how long it stays open. Used by the prompt.user flow: while we're
// waiting on the user (plan_review, permission, AskUserQuestion), the
// surrounding agent / turn / message cards are intentionally idle and
// must not be killed by their own watchdog. Returns true if cardID was
// tracked and not already paused (so the caller can pair it with a
// matching Resume).
func (t *Tracker) Suspend(cardID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.open[cardID]
	if !ok || c.paused {
		return false
	}
	c.paused = true
	return true
}

// Resume reverses Suspend: clears the paused flag and grants the card
// a fresh full timeout window starting now. Granting a fresh window
// (rather than restoring the original deadline minus paused time)
// matches the user-facing semantic — once the user has acted, the
// engine should make progress within the standard timeout, not race a
// stale deadline.
func (t *Tracker) Resume(cardID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.open[cardID]
	if !ok || !c.paused {
		return
	}
	c.paused = false
	c.deadline = t.now().Add(c.timeout)
}

// Touch resets the deadline of cardID and every still-tracked ancestor
// to "now + their original timeout". It's the heartbeat half of the
// watchdog: any activity on a card (and therefore implicitly on its
// containing scope) is evidence the engine is alive, so the orphan
// timeout should restart from zero.
//
// Without this, a step / agent / tool card that legitimately runs
// longer than its registered OrphanTimeoutMs gets killed mid-flight —
// which is what caused the user-visible "执行超时了，我得放弃这步"
// orphan_timeout closes during long plan steps.
//
// Paused cards (see Suspend) are left alone — they already opted out of
// the watchdog and shouldn't have their deadline shortened by an
// unrelated heartbeat.
func (t *Tracker) Touch(cardID string) {
	if cardID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	// Walk along the PERMANENT parent map, not just the still-open cards.
	// When an intermediate ancestor has already been closed (typical for
	// the message card that wrapped a scheduler tool call), we skip
	// the deadline refresh on that gravestone but keep climbing — the
	// turn / plan / step above are still tracked and must not orphan
	// just because a sibling card already finished.
	for cur := cardID; cur != ""; {
		if c, ok := t.open[cur]; ok && !c.paused {
			c.deadline = now.Add(c.timeout)
		}
		parent, known := t.parentOf[cur]
		if !known {
			break
		}
		cur = parent
	}
}

// SuspendChain suspends cardID and walks up its parent chain, suspending
// every still-tracked ancestor it encounters. Returns the list of cards
// that were actually paused (so the caller can hand the same list to
// ResumeChain when the wait ends). Already-closed or untracked ancestors
// are skipped silently — orphaned chains shouldn't propagate errors.
//
// Used by the websocket translator on prompt.user emission: any card
// up the lineage from the agent that triggered the prompt is dormant
// while the human reviews, so its watchdog should not fire.
func (t *Tracker) SuspendChain(cardID string) []string {
	if cardID == "" {
		return nil
	}
	var paused []string
	t.mu.Lock()
	defer t.mu.Unlock()
	for cur := cardID; cur != ""; {
		c, ok := t.open[cur]
		if !ok {
			break
		}
		if !c.paused {
			c.paused = true
			paused = append(paused, cur)
		}
		cur = c.parent
	}
	return paused
}

// ResumeChain is the inverse of SuspendChain. The slice is the set of
// card IDs returned by the matching SuspendChain call.
func (t *Tracker) ResumeChain(cardIDs []string) {
	if len(cardIDs) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, id := range cardIDs {
		c, ok := t.open[id]
		if !ok || !c.paused {
			continue
		}
		c.paused = false
		c.deadline = now.Add(c.timeout)
	}
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
//
// Cards with timeout==0 are "chain-only" entries: registered solely so
// Touch can walk their parent link. They never expire — the watchdog
// skips them. Used for long-lived orchestration tool cards (scheduler,
// task) whose lifetime is bounded by the inner agent run, not by any
// wall-clock budget.
func (t *Tracker) sweep() {
	now := t.now()
	type expired struct {
		cardID string
		oc     *openCard
	}
	var todo []expired
	t.mu.Lock()
	for id, oc := range t.open {
		if oc.paused {
			continue
		}
		if oc.timeout == 0 {
			// chain-only entry — opt out of orphan watchdog
			continue
		}
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
