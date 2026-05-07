package emitv2

import (
	"encoding/json"
	"testing"
	"time"
)

// End-to-end tests reproduce the 5 representative scenarios from
// docs/emit/2026-05-07-protocol-v2.2-card.md §10. Each test drives the
// Builder API as the engine would, then asserts:
//   - the recorded Event sequence matches the documented script
//   - parent_card_id chains form the expected tree
//   - envelope.agent_id is correctly bound (especially under sub-agent scopes)
//   - artifacts surface on the close events as expected
//   - JSON marshal/unmarshal is lossless

func e2eEmitter(t *testing.T) (*Emitter, *RecorderSink, *Tracker) {
	t.Helper()
	rec := NewRecorder()
	tk := NewTracker(TrackerConfig{Now: time.Now})
	em := New(EmitterConfig{
		Sink:       rec,
		SessionID:  "sess_e2e",
		TraceID:    "tr_e2e",
		AgentID:    "main",
		AgentRole:  RolePersona,
		Lifecycle:  tk,
		Now:        func() time.Time { return time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC) },
	})
	return em, rec, tk
}

// 10.1 — pure text reply.
func TestE2E_PureTextConversation(t *testing.T) {
	em, rec, _ := e2eEmitter(t)

	em.Card(CardTurn, "turn_c1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{Role: "assistant", Model: "claude-opus-4-7"})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "Hello")
	em.Card(CardMessage, "msg_1").Append(ChannelText, " World!")
	em.Card(CardMessage, "msg_1").Close(StatusOK,
		WithMetrics(Metrics{TokensOut: 5, DurationMs: 250}),
	)
	em.Card(CardTurn, "turn_c1").Close(StatusOK,
		WithMetrics(Metrics{DurationMs: 856}),
	)

	events := rec.Events()
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6", len(events))
	}

	expectTypes := []EventType{
		EventCardAdd, EventCardAdd, EventCardAppend, EventCardAppend, EventCardClose, EventCardClose,
	}
	for i, want := range expectTypes {
		if events[i].Type != want {
			t.Errorf("event #%d type = %s, want %s", i, events[i].Type, want)
		}
	}

	// Tree: turn_c1 (root) → msg_1 (child)
	if events[0].Envelope.ParentCardID != "" {
		t.Errorf("turn_c1 should have empty parent")
	}
	if events[1].Envelope.ParentCardID != "turn_c1" {
		t.Errorf("msg_1 parent = %q", events[1].Envelope.ParentCardID)
	}

	// Append/close events must inherit the same card_id and parent.
	for i := 2; i < 5; i++ {
		if events[i].Envelope.CardID != "msg_1" {
			t.Errorf("event #%d card_id = %q", i, events[i].Envelope.CardID)
		}
		if events[i].Envelope.ParentCardID != "turn_c1" {
			t.Errorf("event #%d parent = %q", i, events[i].Envelope.ParentCardID)
		}
	}

	// Final close has metrics.
	last := events[5]
	if last.Envelope.CardID != "turn_c1" {
		t.Errorf("last event card_id = %q", last.Envelope.CardID)
	}
	if last.Metrics == nil || last.Metrics.DurationMs != 856 {
		t.Errorf("turn close metrics = %+v", last.Metrics)
	}

	// JSON round-trip.
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed["envelope"].(map[string]any)["trace_id"] != "tr_e2e" {
			t.Errorf("trace_id lost in JSON round-trip")
		}
	}
}

// 10.2 — server-side tool execution.
func TestE2E_ServerToolExecution(t *testing.T) {
	em, rec, _ := e2eEmitter(t)

	em.Card(CardTurn, "turn_c1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{Role: "assistant"})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "Let me check:")
	em.Card(CardMessage, "msg_1").Append(ChannelToolInput, `{"command":`)
	em.Card(CardMessage, "msg_1").Append(ChannelToolInput, `"ls -la"}`)
	em.Card(CardMessage, "msg_1").Close(StatusOK, WithInner(MessagePayload{StopReason: "tool_use"}))

	// Tool was requested by msg_1 (semantic parent), even though msg_1
	// has already closed. Auto-stack would attach to turn_c1; explicit
	// parent preserves the causal link for inline-tool rendering.
	em.Card(CardTool, "toolu_1").Add(ToolPayload{
		Name:   "bash",
		Target: "server",
		Intent: "列目录",
		Input:  map[string]any{"command": "ls -la"},
	}, WithParent("msg_1"))
	em.Card(CardTool, "toolu_1").Close(StatusOK,
		WithInner(ToolPayload{Output: "total 48...", RenderHint: "terminal"}),
		WithMetrics(Metrics{DurationMs: 50}),
	)

	em.Card(CardMessage, "msg_2").Add(MessagePayload{})
	em.Card(CardMessage, "msg_2").Append(ChannelText, "Here are the files:...")
	em.Card(CardMessage, "msg_2").Close(StatusOK)

	em.Card(CardTurn, "turn_c1").Close(StatusOK)

	events := rec.Events()
	tools := rec.FilterByType(EventCardAdd)
	// 4 cards added: turn, msg_1, tool, msg_2
	if len(tools) != 4 {
		t.Fatalf("expected 4 add events, got %d", len(tools))
	}

	// Tool's parent should be msg_1 (the last open card when tool was added).
	for _, ev := range tools {
		if ev.Envelope.CardKind != CardTool {
			continue
		}
		if ev.Envelope.ParentCardID != "msg_1" {
			t.Errorf("tool parent = %q, want msg_1", ev.Envelope.ParentCardID)
		}
	}

	// All events on the same trace.
	for _, ev := range events {
		if ev.Envelope.TraceID != "tr_e2e" {
			t.Errorf("trace drift on event %s", ev.Type)
		}
	}

	// Append events for tool_input use PartialJSON, not Chunk.
	for _, ev := range rec.FilterByType(EventCardAppend) {
		pl := ev.Payload.(AppendPayload)
		if pl.Channel == ChannelToolInput && pl.PartialJSON == "" {
			t.Error("tool_input append should set PartialJSON")
		}
		if pl.Channel == ChannelToolInput && pl.Chunk != "" {
			t.Error("tool_input append should NOT set Chunk")
		}
	}
}

// 10.3 — sub-agent execution. Critical scenario: envelope.agent_id must
// be correctly bound for sub-agent inner tool calls (the v2.2 contract
// that v1.x violated via the subagent.* wrapping layer).
func TestE2E_SubAgentExecution(t *testing.T) {
	em, rec, _ := e2eEmitter(t)

	// Main agent setup
	em.Card(CardTurn, "turn_c1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{Role: "assistant"})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "Let me search...")
	em.Card(CardMessage, "msg_1").Close(StatusOK)

	// emma calls Agent tool, which spawns a sub-agent. Causal parent is
	// msg_1 (the message containing the tool_use directive).
	em.Card(CardTool, "toolu_1").Add(ToolPayload{
		Name:   "Agent",
		Target: "server",
		Intent: "搜索 auth bug",
	}, WithParent("msg_1"))

	// Sub-agent emitter — child Emitter binds new identity but inherits trace.
	sub := em.Sub("sub_e5", RoleWorker, "run_sub_e5")
	sub.Card(CardAgent, "sub_e5").Add(AgentPayload{
		Name:          "search auth bugs",
		AgentType:     "sync",
		ParentAgentID: "main",
	})

	// Sub-agent's inner tool call — directly emits as top-level Card,
	// envelope.agent_id is "sub_e5", parent_card_id = "sub_e5".
	sub.Card(CardTool, "toolu_sub_1").Add(ToolPayload{
		Name:   "Grep",
		Target: "server",
	})
	sub.Card(CardTool, "toolu_sub_1").Tick(TickHeartbeat, HeartbeatPayload{
		Stage: "searching", UptimeMs: 5000,
	})
	sub.Card(CardTool, "toolu_sub_1").Tick(TickProgress, ProgressPayload{
		Stage: "matching", ItemsProcessed: 12, ItemsTotal: 30,
	})
	sub.Card(CardTool, "toolu_sub_1").Close(StatusOK,
		WithInner(ToolPayload{Output: "Found 3"}),
	)
	sub.Card(CardAgent, "sub_e5").Close(StatusOK,
		WithMetrics(Metrics{DurationMs: 8500}),
	)
	em.Card(CardTool, "toolu_1").Close(StatusOK)

	// Final reply
	em.Card(CardMessage, "msg_2").Add(MessagePayload{})
	em.Card(CardMessage, "msg_2").Append(ChannelText, "I found 3 potential...")
	em.Card(CardMessage, "msg_2").Close(StatusOK)
	em.Card(CardTurn, "turn_c1").Close(StatusOK)

	events := rec.Events()

	// Critical contract #1: every event in sub-agent scope carries
	// envelope.agent_id == "sub_e5".
	subAgentCards := []string{"sub_e5", "toolu_sub_1"}
	for _, ev := range events {
		isSubScope := false
		for _, id := range subAgentCards {
			if ev.Envelope.CardID == id {
				isSubScope = true
				break
			}
		}
		if isSubScope && ev.Envelope.AgentID != "sub_e5" {
			t.Errorf("event %s on %s has agent_id=%q, want sub_e5 (CRITICAL CONTRACT)",
				ev.Type, ev.Envelope.CardID, ev.Envelope.AgentID)
		}
	}

	// Critical contract #2: sub-agent's first card auto-attaches as child
	// of parent's open card (toolu_1).
	subAgentAdds := rec.FilterByCard("sub_e5")
	if len(subAgentAdds) == 0 {
		t.Fatal("no events on sub_e5")
	}
	firstSub := subAgentAdds[0]
	if firstSub.Envelope.ParentCardID != "toolu_1" {
		t.Errorf("sub_e5 first event parent = %q, want toolu_1", firstSub.Envelope.ParentCardID)
	}

	// Critical contract #3: tools inside sub-agent attach to sub_e5, not
	// to toolu_1.
	innerToolAdds := rec.FilterByCard("toolu_sub_1")
	for _, ev := range innerToolAdds {
		if ev.Type != EventCardAdd {
			continue
		}
		if ev.Envelope.ParentCardID != "sub_e5" {
			t.Errorf("toolu_sub_1 parent = %q, want sub_e5", ev.Envelope.ParentCardID)
		}
	}

	// Tick events fired during the inner tool call.
	ticks := rec.FilterByType(EventCardTick)
	if len(ticks) != 2 {
		t.Errorf("expected 2 ticks (heartbeat + progress), got %d", len(ticks))
	}
}

// 10.4 — Plan mode + prompt.user(plan_review). Tests the interaction
// flow plus deeper nested sub-agent (3-level: emma → L2 → L3).
func TestE2E_PlanModeWithReview(t *testing.T) {
	em, rec, _ := e2eEmitter(t)

	em.Card(CardTurn, "turn_c1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{Role: "assistant"})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "我来安排专业团帮你...")
	em.Card(CardMessage, "msg_1").Close(StatusOK)

	em.Card(CardTool, "tu_main_1").Add(
		ToolPayload{Name: "Specialists", Intent: "派 Specialists"},
		WithParent("msg_1"),
	)

	// L2 sub-agent
	l2 := em.Sub("sub_y", RoleWorker, "run_l2")
	l2.Card(CardAgent, "sub_y").Add(AgentPayload{Name: "specialists"})

	// Plan review prompt
	reqID := em.PromptUser("plan_review", PlanReviewPromptPayload{
		PlanID: "pln_xxx",
		Goal:   "调研 X 写 Y",
		Steps: []PlanReviewStep{
			{ID: "s1", Description: "调研"},
			{ID: "s2", Description: "撰写", DependsOn: []string{"s1"}},
		},
	})
	em.PromptReply(reqID, "approved", "")

	// Plan executes
	l2.Card(CardPlan, "plan_pln_xxx").Add(PlanPayload{
		PlanID: "pln_xxx", Goal: "调研 X 写 Y", Strategy: "sequential",
		Steps: []PlanStepInfo{{StepID: "s1"}, {StepID: "s2"}},
	})
	l2.Card(CardStep, "step_s1").Add(StepPayload{StepID: "s1", SubagentType: "researcher"})
	l2.Card(CardStep, "step_s1").Set(StepPayload{Status: "running"})

	// L3 sub-agent (writer)
	l3 := l2.Sub("sub_z", RoleWorker, "run_l3")
	l3.Card(CardAgent, "sub_z").Add(AgentPayload{Name: "writer", AgentType: "sync"})
	l3.Card(CardTool, "tu_l3_1").Add(ToolPayload{Name: "ArtifactWrite"})
	l3.Card(CardTool, "tu_l3_1").Close(StatusOK,
		WithInner(ToolPayload{
			Artifacts: []ArtifactRef{{
				ArtifactID: "art_a1b2", Name: "intern-schedule-email.md",
				Type: "file", SizeBytes: 1240, Role: "draft_email",
			}},
		}),
	)
	l3.Card(CardAgent, "sub_z").Close(StatusOK)
	l2.Card(CardStep, "step_s1").Close(StatusOK,
		WithInner(StepPayload{
			OutputSummary: "completed",
			Deliverables:  []string{"art_a1b2"},
		}),
	)
	l2.Card(CardPlan, "plan_pln_xxx").Close(StatusOK)
	l2.Card(CardAgent, "sub_y").Close(StatusOK)
	em.Card(CardTool, "tu_main_1").Close(StatusOK)

	// emma quotes the artifact via markdown URI
	em.Card(CardMessage, "msg_2").Add(MessagePayload{})
	em.Card(CardMessage, "msg_2").Append(ChannelText,
		"邮件已经准备好：[intern-schedule-email.md](artifact://art_a1b2)...")
	em.Card(CardMessage, "msg_2").Close(StatusOK)
	em.Card(CardTurn, "turn_c1").Close(StatusOK)

	// Assertions
	prompts := rec.FilterByType(EventPromptUser)
	if len(prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(prompts))
	}
	pp := prompts[0].Payload.(PromptUserPayload)
	if pp.Kind != "plan_review" {
		t.Errorf("prompt kind = %q", pp.Kind)
	}
	if pp.RequestID != reqID {
		t.Errorf("prompt request_id mismatch")
	}

	replies := rec.FilterByType(EventPromptReply)
	if len(replies) != 1 {
		t.Fatalf("got %d replies", len(replies))
	}

	// Verify 3-level agent_id binding.
	expectAgentID := map[string]string{
		"sub_y": "sub_y", "plan_pln_xxx": "sub_y", "step_s1": "sub_y",
		"sub_z": "sub_z", "tu_l3_1": "sub_z",
	}
	for _, ev := range rec.Events() {
		want, has := expectAgentID[ev.Envelope.CardID]
		if !has {
			continue
		}
		if ev.Envelope.AgentID != want {
			t.Errorf("event on %s: agent_id=%q, want %q", ev.Envelope.CardID, ev.Envelope.AgentID, want)
		}
	}

	// Artifact ref appears on tool close.
	toolCloses := rec.FilterByType(EventCardClose)
	var foundArtifact bool
	for _, ev := range toolCloses {
		pl, ok := ev.Payload.(ClosePayload)
		if !ok || pl.Inner == nil {
			continue
		}
		if tp, ok := pl.Inner.(ToolPayload); ok && len(tp.Artifacts) > 0 {
			if tp.Artifacts[0].ArtifactID == "art_a1b2" {
				foundArtifact = true
			}
		}
	}
	if !foundArtifact {
		t.Error("expected artifact_id art_a1b2 on a tool close")
	}

	// Final message references the artifact via URI.
	msgAppends := rec.FilterByCard("msg_2")
	var foundURI bool
	for _, ev := range msgAppends {
		if ev.Type != EventCardAppend {
			continue
		}
		pl := ev.Payload.(AppendPayload)
		if pl.Channel == ChannelText && containsString(pl.Chunk, "artifact://art_a1b2") {
			foundURI = true
		}
	}
	if !foundURI {
		t.Error("expected emma's reply to reference artifact via artifact:// URI")
	}
}

// 10.5 — long task with progress + heartbeat.
func TestE2E_LongTaskHeartbeats(t *testing.T) {
	em, rec, _ := e2eEmitter(t)

	em.Card(CardTool, "toolu_1").Add(ToolPayload{
		Name: "WebFetch", Intent: "抓取 vLLM 论文",
	})
	em.Card(CardTool, "toolu_1").Tick(TickHeartbeat, HeartbeatPayload{
		Stage: "connecting", UptimeMs: 5000,
	})
	em.Card(CardTool, "toolu_1").Tick(TickHeartbeat, HeartbeatPayload{
		Stage: "fetching", UptimeMs: 10000,
	})
	em.Card(CardTool, "toolu_1").Tick(TickProgress, ProgressPayload{
		Stage: "parsing", ItemsProcessed: 12, ItemsTotal: 30,
	})
	em.Card(CardTool, "toolu_1").Tick(TickProgress, ProgressPayload{
		Stage: "parsing", ItemsProcessed: 25, ItemsTotal: 30, ETAMs: 3000,
	})
	em.Card(CardTool, "toolu_1").Close(StatusOK,
		WithMetrics(Metrics{DurationMs: 30200}),
	)

	ticks := rec.FilterByType(EventCardTick)
	if len(ticks) != 4 {
		t.Errorf("expected 4 ticks, got %d", len(ticks))
	}

	// All ticks belong to toolu_1.
	for _, ev := range ticks {
		if ev.Envelope.CardID != "toolu_1" {
			t.Errorf("tick card_id = %q", ev.Envelope.CardID)
		}
	}

	// The last progress tick has eta_ms.
	lastTick := ticks[3].Payload.(TickPayload)
	pp := lastTick.Inner.(ProgressPayload)
	if pp.ETAMs != 3000 {
		t.Errorf("last tick ETAMs = %d, want 3000", pp.ETAMs)
	}
}

// containsString is a tiny helper to avoid importing strings just for this.
func containsString(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
