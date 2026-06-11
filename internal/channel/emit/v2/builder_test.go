package emitv2

import (
	"strings"
	"testing"
	"time"
)

// fixedClock returns a deterministic time for tests.
func fixedClock() time.Time {
	return time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
}

// newTestEmitter builds an Emitter with deterministic clock and a recorder
// sink. Returns the emitter and the recorder for assertions.
func newTestEmitter(t *testing.T) (*Emitter, *RecorderSink) {
	t.Helper()
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink:      rec,
		SessionID: "sess_test",
		TraceID:   "tr_test",
		AgentID:   "main",
		AgentRole: RolePersona,
		Now:       fixedClock,
	})
	return em, rec
}

func TestEmitter_RequiresSink(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when Sink is nil")
		}
	}()
	New(EmitterConfig{SessionID: "s"})
}

func TestEmitter_RequiresSessionID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when SessionID is empty")
		}
	}()
	New(EmitterConfig{Sink: NewRecorder()})
}

func TestEmitter_AllocatesTraceIDIfEmpty(t *testing.T) {
	em := New(EmitterConfig{Sink: NewRecorder(), SessionID: "s"})
	if !strings.HasPrefix(em.TraceID(), "tr_") {
		t.Errorf("auto trace ID should start with tr_, got %q", em.TraceID())
	}
}

func TestCardAdd_FillsEnvelope(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "turn_1").Add(TurnPayload{TurnNo: 1})

	got := rec.Events()
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	ev := got[0]
	if ev.Type != EventCardAdd {
		t.Errorf("Type = %s, want %s", ev.Type, EventCardAdd)
	}
	if ev.Envelope.SessionID != "sess_test" {
		t.Errorf("SessionID = %q", ev.Envelope.SessionID)
	}
	if ev.Envelope.TraceID != "tr_test" {
		t.Errorf("TraceID = %q", ev.Envelope.TraceID)
	}
	if ev.Envelope.CardID != "turn_1" {
		t.Errorf("CardID = %q", ev.Envelope.CardID)
	}
	if ev.Envelope.CardKind != CardTurn {
		t.Errorf("CardKind = %s", ev.Envelope.CardKind)
	}
	if ev.Envelope.Seq != 1 {
		t.Errorf("Seq = %d, want 1", ev.Envelope.Seq)
	}
	if !ev.Envelope.Timestamp.Equal(fixedClock()) {
		t.Errorf("Timestamp = %v", ev.Envelope.Timestamp)
	}
	if ev.Envelope.AgentRole != RolePersona {
		t.Errorf("AgentRole = %s", ev.Envelope.AgentRole)
	}
	if ev.Envelope.Severity != SeverityInfo {
		t.Errorf("Severity = %s, want info on add", ev.Envelope.Severity)
	}
}

func TestCardAdd_HintTitleFromRegistryTemplate(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "turn_1").Add(TurnPayload{TurnNo: 3})
	ev := rec.Events()[0]
	if ev.Hint == nil || !strings.Contains(ev.Hint.Title, "3") {
		t.Errorf("expected Hint.Title to template TurnNo=3, got %v", ev.Hint)
	}
}

func TestCardAdd_HintIconFromRegistry(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "bash"})
	ev := rec.Events()[0]
	if ev.Hint == nil || ev.Hint.Icon != "tool" {
		t.Errorf("expected Tool default icon, got %v", ev.Hint)
	}
	if ev.Hint.Title != "bash" {
		t.Errorf("expected Tool title=tool_name=Bash, got %q", ev.Hint.Title)
	}
}

func TestCardAdd_HintOverride(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(
		ToolPayload{Name: "bash"},
		WithHint(Hint{Title: "Custom", Icon: "fire"}),
	)
	ev := rec.Events()[0]
	if ev.Hint.Title != "Custom" {
		t.Errorf("Hint override title=%q", ev.Hint.Title)
	}
	if ev.Hint.Icon != "fire" {
		t.Errorf("Hint override icon=%q", ev.Hint.Icon)
	}
}

func TestCardAdd_AutoParentFromStack(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "turn_1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{Role: "assistant"})

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Envelope.ParentCardID != "" {
		t.Errorf("turn_1 should have no parent, got %q", events[0].Envelope.ParentCardID)
	}
	if events[1].Envelope.ParentCardID != "turn_1" {
		t.Errorf("msg_1 should auto-attach to turn_1, got %q", events[1].Envelope.ParentCardID)
	}
}

func TestCardAdd_ParentOverride(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "turn_1").Add(TurnPayload{})
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "bash"}, WithParent("explicit_parent"))
	tools := rec.FilterByType(EventCardAdd)
	if got := tools[1].Envelope.ParentCardID; got != "explicit_parent" {
		t.Errorf("explicit parent override failed, got %q", got)
	}
}

func TestCardClose_PopsParentStack(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "turn_1").Add(TurnPayload{})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{})
	em.Card(CardMessage, "msg_1").Close(StatusOK)
	em.Card(CardMessage, "msg_2").Add(MessagePayload{})

	events := rec.Events()
	// After msg_1 closes, msg_2 should attach back to turn_1, not msg_1.
	last := events[len(events)-1]
	if last.Envelope.ParentCardID != "turn_1" {
		t.Errorf("after msg_1.Close, msg_2.parent = %q, want turn_1", last.Envelope.ParentCardID)
	}
}

func TestCardClose_FailedStatusSetsErrorSeverity(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "bash"})
	em.Card(CardTool, "tool_1").Close(StatusFailed,
		WithError(NewError(ErrorTypeToolTimeout, "timed out")),
	)
	closes := rec.FilterByType(EventCardClose)
	if len(closes) != 1 {
		t.Fatalf("got %d closes", len(closes))
	}
	if closes[0].Envelope.Severity != SeverityError {
		t.Errorf("failed close severity = %s, want error", closes[0].Envelope.Severity)
	}
}

func TestCardClose_OKStatusSetsInfoSeverity(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "bash"})
	em.Card(CardTool, "tool_1").Close(StatusOK)
	closes := rec.FilterByType(EventCardClose)
	if closes[0].Envelope.Severity != SeverityInfo {
		t.Errorf("ok close severity = %s, want info", closes[0].Envelope.Severity)
	}
}

func TestCardClose_PayloadCarriesError(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "bash"})
	em.Card(CardTool, "tool_1").Close(StatusFailed,
		WithError(NewError(ErrorTypeToolTimeout, "timed out")),
	)
	closes := rec.FilterByType(EventCardClose)
	pl, ok := closes[0].Payload.(ClosePayload)
	if !ok {
		t.Fatalf("close payload type = %T", closes[0].Payload)
	}
	if pl.Status != StatusFailed {
		t.Errorf("Status = %s", pl.Status)
	}
	if pl.Error == nil {
		t.Fatal("Error block missing")
	}
	if pl.Error.UserMessage == "" {
		t.Error("ErrorInfo.UserMessage should be auto-filled from registry")
	}
	if !pl.Error.Retryable {
		t.Error("tool_timeout should default Retryable=true")
	}
}

func TestCardAppend_TextChannel(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardMessage, "msg_1").Add(MessagePayload{})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "Hello")
	em.Card(CardMessage, "msg_1").Append(ChannelText, " world")

	appends := rec.FilterByType(EventCardAppend)
	if len(appends) != 2 {
		t.Fatalf("got %d appends", len(appends))
	}
	for _, ev := range appends {
		pl := ev.Payload.(AppendPayload)
		if pl.Channel != ChannelText {
			t.Errorf("channel = %s", pl.Channel)
		}
	}
	first := appends[0].Payload.(AppendPayload)
	if first.Chunk != "Hello" {
		t.Errorf("first chunk = %q", first.Chunk)
	}
}

func TestCardAppend_ToolInputUsesPartialJSON(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardMessage, "msg_1").Add(MessagePayload{})
	em.Card(CardMessage, "msg_1").Append(ChannelToolInput, `{"command":`)
	pl := rec.FilterByType(EventCardAppend)[0].Payload.(AppendPayload)
	if pl.PartialJSON != `{"command":` {
		t.Errorf("PartialJSON = %q", pl.PartialJSON)
	}
	if pl.Chunk != "" {
		t.Errorf("Chunk should be empty for tool_input, got %q", pl.Chunk)
	}
}

func TestCardTick_Progress(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "web_fetch"})
	em.Card(CardTool, "tool_1").Tick(TickProgress, ProgressPayload{
		ItemsProcessed: 12, ItemsTotal: 30, ETAMs: 3000,
	})
	ticks := rec.FilterByType(EventCardTick)
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks", len(ticks))
	}
	tp := ticks[0].Payload.(TickPayload)
	if tp.Kind != TickProgress {
		t.Errorf("Kind = %s", tp.Kind)
	}
	pp := tp.Inner.(ProgressPayload)
	if pp.ItemsTotal != 30 {
		t.Errorf("ItemsTotal = %d", pp.ItemsTotal)
	}
}

func TestSeqIsMonotonic(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTurn, "t1").Add(TurnPayload{})
	em.Card(CardMessage, "m1").Add(MessagePayload{})
	em.Card(CardMessage, "m1").Append(ChannelText, "a")
	em.Card(CardMessage, "m1").Append(ChannelText, "b")
	em.Card(CardMessage, "m1").Close(StatusOK)
	em.Card(CardTurn, "t1").Close(StatusOK)

	prev := int64(0)
	for i, ev := range rec.Events() {
		if ev.Envelope.Seq <= prev {
			t.Errorf("event #%d seq = %d, prev = %d (not monotonic)", i, ev.Envelope.Seq, prev)
		}
		prev = ev.Envelope.Seq
	}
}

func TestEmitter_Sub_BindsAgentIdentityAndKeepsTrace(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "Agent"})

	child := em.Sub("sub_e5", RoleWorker, "run_a")
	child.Card(CardAgent, "sub_e5").Add(AgentPayload{Name: "researcher", AgentType: "sync"})
	child.Card(CardTool, "tool_inner").Add(ToolPayload{Name: "grep"})

	allEvents := rec.Events()
	if len(allEvents) != 3 {
		t.Fatalf("got %d events", len(allEvents))
	}

	// Trace ID must be shared.
	for _, ev := range allEvents {
		if ev.Envelope.TraceID != "tr_test" {
			t.Errorf("event %s: TraceID drifted to %q", ev.Type, ev.Envelope.TraceID)
		}
	}
	// Agent identity differs between parent and child.
	if allEvents[0].Envelope.AgentID != "main" {
		t.Errorf("parent event AgentID = %q, want main", allEvents[0].Envelope.AgentID)
	}
	if allEvents[1].Envelope.AgentID != "sub_e5" {
		t.Errorf("child agent event AgentID = %q, want sub_e5", allEvents[1].Envelope.AgentID)
	}
	if allEvents[2].Envelope.AgentID != "sub_e5" {
		t.Errorf("child tool event AgentID = %q, want sub_e5 (CRITICAL contract)", allEvents[2].Envelope.AgentID)
	}
	// Sub-agent's first card auto-attaches as child of parent's open card.
	if allEvents[1].Envelope.ParentCardID != "tool_1" {
		t.Errorf("sub-agent first card parent = %q, want tool_1", allEvents[1].Envelope.ParentCardID)
	}
	// Within child, subsequent card.add inherits sub_e5 as parent.
	if allEvents[2].Envelope.ParentCardID != "sub_e5" {
		t.Errorf("child tool parent = %q, want sub_e5", allEvents[2].Envelope.ParentCardID)
	}
}

func TestPromptUser_AllocatesRequestID(t *testing.T) {
	em, rec := newTestEmitter(t)
	id := em.PromptUser("permission", PermissionPromptPayload{ToolName: "bash"})
	if !strings.HasPrefix(id, "req_") {
		t.Errorf("request ID = %q, want req_ prefix", id)
	}
	prompts := rec.FilterByType(EventPromptUser)
	if len(prompts) != 1 {
		t.Fatalf("got %d prompts", len(prompts))
	}
	pl := prompts[0].Payload.(PromptUserPayload)
	if pl.RequestID != id {
		t.Errorf("payload request_id = %q, expected %q", pl.RequestID, id)
	}
	if pl.Kind != "permission" {
		t.Errorf("payload kind = %q", pl.Kind)
	}
}

func TestSessionEvent_ErrorSetsErrorSeverity(t *testing.T) {
	em, rec := newTestEmitter(t)
	em.SessionEvent("error", map[string]string{"reason": "auth_failed"})
	got := rec.Events()
	if got[0].Envelope.Severity != SeverityError {
		t.Errorf("session.error severity = %s, want error", got[0].Envelope.Severity)
	}
}
