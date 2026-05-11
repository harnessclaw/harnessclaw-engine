package emitv2

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestTracker_OpenCloseRemovesEntry(t *testing.T) {
	now := time.Now()
	tk := NewTracker(TrackerConfig{Now: func() time.Time { return now }})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s", TraceID: "tr_test",
		Lifecycle: tk, Now: func() time.Time { return now },
	})

	em.Card(CardStep, "step_a").Add(StepPayload{StepID: "a"})
	if tk.OpenCount() != 1 {
		t.Errorf("OpenCount after Add = %d, want 1", tk.OpenCount())
	}
	em.Card(CardStep, "step_a").Close(StatusOK)
	if tk.OpenCount() != 0 {
		t.Errorf("OpenCount after Close = %d, want 0", tk.OpenCount())
	}
}

func TestTracker_ParentOf(t *testing.T) {
	now := time.Now()
	tk := NewTracker(TrackerConfig{Now: func() time.Time { return now }})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: func() time.Time { return now },
	})

	em.Card(CardTurn, "turn_a").Add(TurnPayload{})
	em.Card(CardMessage, "msg_a").Add(MessagePayload{})

	if got := tk.ParentOf("msg_a"); got != "turn_a" {
		t.Errorf("ParentOf(msg_a) = %q, want turn_a", got)
	}
	if got := tk.ParentOf("turn_a"); got != "" {
		t.Errorf("ParentOf(turn_a) = %q, want empty (root)", got)
	}
	if got := tk.ParentOf("missing"); got != "" {
		t.Errorf("ParentOf(missing) = %q, want empty", got)
	}
}

func TestTracker_OrphanTimeoutSyntheticClose(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now, CheckEvery: 10 * time.Millisecond})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardStep, "step_a").Add(StepPayload{StepID: "a", SubagentType: "researcher"})

	// Advance clock past CardStep's orphan timeout (5 min by registry).
	clk.advance(6 * time.Minute)
	tk.SweepNow()

	closes := rec.FilterByType(EventCardClose)
	if len(closes) != 1 {
		t.Fatalf("got %d synthetic closes, want 1", len(closes))
	}
	pl := closes[0].Payload.(ClosePayload)
	if pl.Status != StatusFailed {
		t.Errorf("synthetic close status = %s, want failed", pl.Status)
	}
	if pl.Error == nil || pl.Error.Type != ErrorTypeOrphanTimeout {
		t.Errorf("error.type = %v, want orphan_timeout", pl.Error)
	}
	if pl.Error.UserMessage == "" {
		t.Error("synthetic close should have UserMessage from registry")
	}
	if tk.OpenCount() != 0 {
		t.Errorf("OpenCount after sweep = %d, want 0", tk.OpenCount())
	}
}

func TestTracker_NotYetExpiredStaysOpen(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardStep, "step_a").Add(StepPayload{StepID: "a"})
	clk.advance(10 * time.Second) // CardStep timeout is 30m
	tk.SweepNow()

	if tk.OpenCount() != 1 {
		t.Errorf("OpenCount = %d, want 1 (not yet expired)", tk.OpenCount())
	}
	if len(rec.FilterByType(EventCardClose)) != 0 {
		t.Error("synthetic close should not have fired yet")
	}
}

func TestTracker_UntrackedKindNotWatchdogged(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardArtifact, "art_a").Add(ArtifactPayload{ArtifactID: "art_a", Name: "x.md"})
	if tk.OpenCount() != 0 {
		t.Errorf("OpenCount for untracked kind = %d, want 0", tk.OpenCount())
	}
}

// Suspended cards must NOT be killed by the watchdog no matter how long
// they stay open. This pins the contract behind the "prompt.user has no
// timeout" design — while waiting on a user response, the surrounding
// agent / message / turn cards are paused and survive arbitrary delay.
func TestTracker_SuspendedCardSurvivesPastDeadline(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardAgent, "agent_a").Add(AgentPayload{Name: "worker"})
	if !tk.Suspend("agent_a") {
		t.Fatal("Suspend(agent_a) returned false; expected true on first call")
	}
	// CardAgent timeout is 10 min — advance well past it.
	clk.advance(30 * time.Minute)
	tk.SweepNow()

	if got := len(rec.FilterByType(EventCardClose)); got != 0 {
		t.Fatalf("synthetic close fired while suspended; got %d", got)
	}
	if tk.OpenCount() != 1 {
		t.Errorf("OpenCount = %d, want 1 (paused card stays tracked)", tk.OpenCount())
	}

	// Resume must restore the watchdog with a fresh full window — past
	// dwell time should not count against the new deadline.
	tk.Resume("agent_a")
	clk.advance(30 * time.Second) // small step inside the fresh window
	tk.SweepNow()
	if got := len(rec.FilterByType(EventCardClose)); got != 0 {
		t.Errorf("synthetic close fired right after Resume; got %d", got)
	}
}

// SuspendChain / ResumeChain walk the parent chain and pause every
// tracked ancestor. Verifies the websocket translator's strategy:
// suspending the worker's agent card transitively keeps the message and
// turn cards alive for the duration of a plan_review.
func TestTracker_SuspendChainPausesAncestors(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardTurn, "turn_a").Add(TurnPayload{})
	em.Card(CardMessage, "msg_a").Add(MessagePayload{})
	em.Card(CardAgent, "agent_a").Add(AgentPayload{Name: "worker"})

	paused := em.SuspendChainFromCard("agent_a")
	if len(paused) != 3 {
		t.Fatalf("SuspendChainFromCard returned %d cards, want 3 (agent → msg → turn)", len(paused))
	}

	// Past every kind's timeout — none should fire.
	clk.advance(2 * time.Hour)
	tk.SweepNow()
	if got := len(rec.FilterByType(EventCardClose)); got != 0 {
		t.Errorf("synthetic close fired while chain was suspended; got %d", got)
	}

	em.ResumeChain(paused)
	// After resume each card has timeout-from-now. Only push past the
	// shortest (CardMessage = 5 min) and confirm the watchdog wakes up.
	clk.advance(6 * time.Minute)
	tk.SweepNow()
	closes := rec.FilterByType(EventCardClose)
	if len(closes) == 0 {
		t.Fatal("expected at least the message card to orphan-timeout after resume")
	}
}

// Heartbeat: any activity on a tracked card (set/append/tick, or a child
// opening) must reset the deadline, otherwise long-running steps and
// agents get killed mid-flight by the watchdog. The user-facing bug
// this guards: "card had no close before orphan timeout: agent" / step
// firing during normal plan execution.
func TestTracker_HeartbeatResetsDeadline(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardTool, "tool_a").Add(ToolPayload{Name: "Bash"})
	// CardTool timeout is 120s. Advance to 90s (still alive), heartbeat
	// via Tick, then advance another 90s — total 180s, but each window
	// from the last Touch was only 90s, so it should still be alive.
	clk.advance(90 * time.Second)
	em.Card(CardTool, "tool_a").Tick(TickProgress, ProgressPayload{})
	clk.advance(90 * time.Second)
	tk.SweepNow()
	if got := len(rec.FilterByType(EventCardClose)); got != 0 {
		t.Errorf("watchdog killed a heart-beating card; got %d synthetic closes", got)
	}
	if tk.OpenCount() != 1 {
		t.Errorf("OpenCount = %d, want 1 (heartbeat kept it alive)", tk.OpenCount())
	}

	// Stop heart-beating: now the card should die at its full timeout
	// from the last activity.
	clk.advance(2 * time.Minute) // > 120s
	tk.SweepNow()
	if got := len(rec.FilterByType(EventCardClose)); got == 0 {
		t.Error("watchdog did not fire after the heartbeat stopped")
	}
}

// Heartbeat propagates up: activity on a child resets the parents'
// deadlines too. Required because long-running plan executions show
// activity on inner tool / message cards, but the outer agent / turn
// cards would otherwise sit silent and get orphan-timed-out.
func TestTracker_HeartbeatPropagatesToAncestors(t *testing.T) {
	clk := atomicClock{t: time.Now()}
	tk := NewTracker(TrackerConfig{Now: clk.Now})
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink: rec, SessionID: "s",
		Lifecycle: tk, Now: clk.Now,
	})

	em.Card(CardAgent, "agent_a").Add(AgentPayload{Name: "worker"})
	em.Card(CardTool, "tool_a").Add(ToolPayload{Name: "Bash"})

	// Heartbeat the tool every 90s for 15 minutes total. CardAgent's
	// orphan timeout is 10 min, so without the chain heartbeat the
	// agent card would die well before this loop ends. Every Append
	// on the child must reset both tool_a (2 min) and agent_a (10 min)
	// deadlines — otherwise tool_a dies first and the chain breaks.
	for i := 0; i < 10; i++ {
		clk.advance(90 * time.Second)
		em.Card(CardTool, "tool_a").Append(ChannelText, "still working")
		tk.SweepNow()
	}
	if got := len(rec.FilterByType(EventCardClose)); got != 0 {
		t.Errorf("watchdog killed a heart-beating chain; got %d synthetic closes", got)
	}
	if tk.OpenCount() != 2 {
		t.Errorf("OpenCount = %d, want 2 (both kept alive)", tk.OpenCount())
	}

	// Closing the child also counts as activity on the parent.
	em.Card(CardTool, "tool_a").Close(StatusOK)
	clk.advance(9 * time.Minute) // < 10 min agent timeout from last touch
	tk.SweepNow()
	for _, e := range rec.FilterByType(EventCardClose) {
		pl := e.Payload.(ClosePayload)
		if pl.Error != nil && pl.Error.Type == ErrorTypeOrphanTimeout {
			t.Errorf("orphan_timeout fired for %s after child close heartbeat", e.Envelope.CardID)
		}
	}
}

func TestTracker_StartStop(t *testing.T) {
	tk := NewTracker(TrackerConfig{CheckEvery: 1 * time.Millisecond})
	tk.Start()
	time.Sleep(5 * time.Millisecond)
	tk.Stop()
	tk.Stop() // double stop is safe
}

// atomicClock is a thread-safe injectable clock for lifecycle tests.
type atomicClock struct {
	t  time.Time
	ns int64 // additional offset in nanoseconds
}

func (c *atomicClock) Now() time.Time {
	return c.t.Add(time.Duration(atomic.LoadInt64(&c.ns)))
}

func (c *atomicClock) advance(d time.Duration) {
	atomic.AddInt64(&c.ns, int64(d))
}
