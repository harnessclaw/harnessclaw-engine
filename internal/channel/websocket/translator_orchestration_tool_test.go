package websocket

// translator_orchestration_tool_test.go — regression guard for the
// WithoutLifecycle() opt-out path.
//
// Background: EngineEventToolStart with ToolName="task" or "scheduler"
// calls isOrchestrationTool() and appends emitv2.WithoutLifecycle() to
// the emit opts. Builder.Add() then calls lifecycle.Open(..., timeout=0,
// ...)  instead of the normal 120 s CardTool timeout. The sweep loop
// skips entries with timeout==0, so the watchdog never fires a synthetic
// orphan_timeout close for these long-lived orchestration tool cards.
//
// These three tests verify that path end-to-end via a Tracker with an
// injectable clock and SweepNow() — no real sleeping required.

import (
	"sync/atomic"
	"testing"
	"time"

	emitv2 "harnessclaw-go/internal/channel/emit/v2"
	"harnessclaw-go/pkg/types"
)

// testClock is a thread-safe injectable clock for these tests.
// (The emitv2-internal atomicClock lives in a _test.go inside package
// emitv2 and is therefore not exported; we replicate the tiny struct here.)
type testClock struct {
	base time.Time
	ns   int64 // atomic offset in nanoseconds
}

func newTestClock() *testClock { return &testClock{base: time.Now()} }

func (c *testClock) Now() time.Time {
	return c.base.Add(time.Duration(atomic.LoadInt64(&c.ns)))
}

func (c *testClock) advance(d time.Duration) {
	atomic.AddInt64(&c.ns, int64(d))
}

// makeTrackedEmitterWithClock wires a Tracker and Emitter that share
// the same injectable clock. Both must see the same "current time" or
// deadline comparisons inside sweep() produce incorrect results.
func makeTrackedEmitterWithClock(t *testing.T, sessionID string, clk *testClock) (
	*emitv2.Emitter, *emitv2.RecorderSink, *emitv2.Tracker,
) {
	t.Helper()
	rec := emitv2.NewRecorder()
	tk := emitv2.NewTracker(emitv2.TrackerConfig{Now: clk.Now})
	em := emitv2.New(emitv2.EmitterConfig{
		Sink:      rec,
		SessionID: sessionID,
		AgentID:   "main",
		AgentRole: emitv2.RolePersona,
		Lifecycle: tk,
		Now:       clk.Now,
	})
	return em, rec, tk
}

// cardToolOrphanTimeout is the registry value for CardTool (120 s).
// We advance past it by a healthy margin to confirm the sweep would have
// fired had the card been normally tracked.
const cardToolOrphanTimeout = 120 * time.Second

// TestTranslator_TaskToolCardIsLifecycleExempt verifies that a tool card
// opened for ToolName="task" does NOT receive a synthetic orphan_timeout
// close even after the 120 s CardTool watchdog deadline has elapsed.
// The fix: translator appends WithoutLifecycle() → Builder.Add registers
// timeout=0 (chain-only) → sweep() skips it.
func TestTranslator_TaskToolCardIsLifecycleExempt(t *testing.T) {
	clk := newTestClock()
	em, rec, tk := makeTrackedEmitterWithClock(t, "sess_task", clk)
	tr := NewTranslator(nil)

	// Open a message first so the tool has a natural parent.
	tr.Translate(em, "sess_task", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_task",
	})

	// Fire the orchestration tool event.
	tr.Translate(em, "sess_task", &types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolName:  "freelance",
		ToolUseID: "toolu_task_1",
		ToolInput: `{"goal":"write a report"}`,
	})

	// The tool card must be registered in the tracker (chain-only).
	// OpenCount includes: turn (auto-opened) + message + tool card = 3.
	if tk.OpenCount() < 1 {
		t.Fatalf("expected tracker to have at least the tool card open, OpenCount=%d", tk.OpenCount())
	}

	// Advance time well past CardTool's 120 s orphan timeout.
	clk.advance(5 * time.Minute)

	// Synchronously sweep — no real sleep needed.
	tk.SweepNow()

	// No orphan_timeout close must have been emitted for the Task tool card.
	for _, ev := range rec.FilterByCard("toolu_task_1") {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			continue
		}
		if pl.Error != nil && pl.Error.Type == emitv2.ErrorTypeOrphanTimeout {
			t.Errorf("Task tool card received orphan_timeout close — WithoutLifecycle() opt-out is broken")
		}
	}

	// Sanity: the card should still be tracked (chain-only, timeout=0 →
	// sweep leaves it alone but doesn't remove it from open map).
	// If it was incorrectly removed by sweep the OpenCount would have dropped.
	// We count it indirectly: SweepNow would have deleted and fired close
	// if timeout > 0 and deadline passed; since no close fired, the entry
	// still lives. We can also verify via Suspend returning false only if
	// NOT paused (but the point here is absence of close, which we checked).
}

// TestTranslator_SchedulerToolCardIsLifecycleExempt is the same guard
// for ToolName="scheduler" — the other name in isOrchestrationTool().
func TestTranslator_SchedulerToolCardIsLifecycleExempt(t *testing.T) {
	clk := newTestClock()
	em, rec, tk := makeTrackedEmitterWithClock(t, "sess_spec", clk)
	tr := NewTranslator(nil)

	tr.Translate(em, "sess_spec", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_spec",
	})
	tr.Translate(em, "sess_spec", &types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolName:  "scheduler",
		ToolUseID: "toolu_spec_1",
		ToolInput: `{"task":"coordinate"}`,
	})

	if tk.OpenCount() < 1 {
		t.Fatalf("expected tracker to have at least the tool card open, OpenCount=%d", tk.OpenCount())
	}

	// Advance well past CardTool 120 s timeout.
	clk.advance(5 * time.Minute)
	tk.SweepNow()

	for _, ev := range rec.FilterByCard("toolu_spec_1") {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			continue
		}
		if pl.Error != nil && pl.Error.Type == emitv2.ErrorTypeOrphanTimeout {
			t.Errorf("scheduler tool card received orphan_timeout close — WithoutLifecycle() opt-out is broken")
		}
	}
}

// TestTranslator_TaskToolCardCallPathIsLifecycleExempt is the same guard
// for the EngineEventToolCall code path (client-side tool calls). The
// ToolStart path already had the WithoutLifecycle() fix; this test covers
// the parallel gap on the ToolCall path that caused the 22:42:29 orphan_timeout.
func TestTranslator_TaskToolCardCallPathIsLifecycleExempt(t *testing.T) {
	clk := newTestClock()
	em, rec, tk := makeTrackedEmitterWithClock(t, "sess_task_call", clk)
	tr := NewTranslator(nil)

	// Open a message first so the tool has a natural parent.
	tr.Translate(em, "sess_task_call", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_task_call",
	})

	// Fire the orchestration tool via the ToolCall path (not ToolStart).
	tr.Translate(em, "sess_task_call", &types.EngineEvent{
		Type:      types.EngineEventToolCall,
		ToolName:  "freelance",
		ToolUseID: "toolu_task_call_1",
		ToolInput: `{"goal":"write a report"}`,
	})

	// The tool card must be registered in the tracker.
	if tk.OpenCount() < 1 {
		t.Fatalf("expected tracker to have at least the tool card open, OpenCount=%d", tk.OpenCount())
	}

	// Advance well past CardTool 120 s orphan timeout.
	clk.advance(5 * time.Minute)
	tk.SweepNow()

	// No orphan_timeout close must have been emitted for the Task tool card.
	for _, ev := range rec.FilterByCard("toolu_task_call_1") {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			continue
		}
		if pl.Error != nil && pl.Error.Type == emitv2.ErrorTypeOrphanTimeout {
			t.Errorf("Task tool card (ToolCall path) received orphan_timeout close — WithoutLifecycle() opt-out missing on EngineEventToolCall branch")
		}
	}
}

// TestTranslator_TaskToolCardViaSubAgentEventIsLifecycleExempt covers the
// nested-dispatch path: scheduler (itself a sub-agent) calls Task. Its
// tool_start arrives wrapped in EngineEventSubAgentEvent, which historically
// did NOT route through isOrchestrationTool() — so the Task card got the
// default 120 s CardTool timeout and was killed mid-run while plan-coord
// kept working for 6+ minutes. The fix appends WithoutLifecycle() in
// translateSubAgentEvent's tool_start branch.
func TestTranslator_TaskToolCardViaSubAgentEventIsLifecycleExempt(t *testing.T) {
	clk := newTestClock()
	em, rec, tk := makeTrackedEmitterWithClock(t, "sess_subevt", clk)
	tr := NewTranslator(nil)

	// Open a turn + a sub-agent so SubAgentEvent has a registered child emitter.
	tr.Translate(em, "sess_subevt", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_subevt",
	})
	tr.Translate(em, "sess_subevt", &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_scheduler",
		AgentName: "scheduler",
		AgentType: "sync",
	})

	// scheduler (sub-agent) dispatches Task — the event arrives wrapped.
	tr.Translate(em, "sess_subevt", &types.EngineEvent{
		Type:    types.EngineEventSubAgentEvent,
		AgentID: "agent_scheduler",
		SubAgentEvent: &types.SubAgentEventData{
			EventType: "tool_start",
			ToolName:  "freelance",
			ToolUseID: "toolu_nested_task",
			ToolInput: `{"subagent_type":"freelancer","prompt":"..."}`,
		},
	})

	if tk.OpenCount() < 1 {
		t.Fatalf("expected tracker to have the nested Task card open, OpenCount=%d", tk.OpenCount())
	}

	// Advance well past 120 s — if opt-out is broken, sweep will synthesise
	// a failed close on the Task card here.
	clk.advance(5 * time.Minute)
	tk.SweepNow()

	for _, ev := range rec.FilterByCard("toolu_nested_task") {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			continue
		}
		if pl.Error != nil && pl.Error.Type == emitv2.ErrorTypeOrphanTimeout {
			t.Errorf("nested Task tool card (via SubAgentEvent) received orphan_timeout close — opt-out missing on translateSubAgentEvent tool_start branch")
		}
	}
}

// TestTranslator_RegularToolCardOrphansAfterTimeout is the control:
// wires Bash tool, advances past 120 s, sweeps, asserts orphan close fired.
func TestTranslator_RegularToolCardOrphansAfterTimeout(t *testing.T) {
	clk := newTestClock()
	em, rec, tk := makeTrackedEmitterWithClock(t, "sess_ctrl", clk)
	tr := NewTranslator(nil)

	tr.Translate(em, "sess_ctrl", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_ctrl",
	})
	tr.Translate(em, "sess_ctrl", &types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolName:  "bash",
		ToolUseID: "toolu_ctrl_1",
		ToolInput: `{"command":"sleep 9999"}`,
	})

	// Advance past the 120 s CardTool orphan timeout.
	clk.advance(cardToolOrphanTimeout + 30*time.Second)
	tk.SweepNow()

	// A synthetic orphan_timeout close MUST have been emitted for the Bash card.
	var found bool
	for _, ev := range rec.FilterByCard("toolu_ctrl_1") {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			continue
		}
		if pl.Error != nil && pl.Error.Type == emitv2.ErrorTypeOrphanTimeout {
			found = true
			break
		}
	}
	if !found {
		t.Error("control test FAILED: Bash tool card did not receive orphan_timeout close — clock injection or SweepNow is broken; the exempt tests above cannot be trusted")
	}
}
