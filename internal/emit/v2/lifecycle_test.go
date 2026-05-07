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

	// Advance clock past CardStep's orphan timeout (60s by registry).
	clk.advance(2 * time.Minute)
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
	clk.advance(10 * time.Second) // CardStep timeout is 60s
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
