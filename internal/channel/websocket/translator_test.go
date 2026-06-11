package websocket

import (
	"context"
	"reflect"
	"testing"

	emitv2 "harnessclaw-go/internal/channel/emit/v2"
	"harnessclaw-go/pkg/types"
)

// makeRecorderEmitter wraps emit/v2 RecorderSink in an Emitter so tests
// can drive the translator and read the produced wire frames straight
// from a slice.
func makeRecorderEmitter(t *testing.T, sessionID string) (*emitv2.Emitter, *emitv2.RecorderSink) {
	t.Helper()
	rec := emitv2.NewRecorder()
	em := emitv2.New(emitv2.EmitterConfig{
		Sink:      rec,
		SessionID: sessionID,
		AgentID:   "main",
		AgentRole: emitv2.RolePersona,
	})
	return em, rec
}

// makeTrackedEmitter is the same as makeRecorderEmitter but with an
// orphan-watchdog Tracker wired in. Returns the tracker so tests can
// drive its clock and inspect OpenCount.
func makeTrackedEmitter(t *testing.T, sessionID string) (*emitv2.Emitter, *emitv2.RecorderSink, *emitv2.Tracker) {
	t.Helper()
	rec := emitv2.NewRecorder()
	tk := emitv2.NewTracker(emitv2.TrackerConfig{})
	em := emitv2.New(emitv2.EmitterConfig{
		Sink:      rec,
		SessionID: sessionID,
		AgentID:   "main",
		AgentRole: emitv2.RolePersona,
		Lifecycle: tk,
	})
	return em, rec, tk
}

// findClosePayload returns the ClosePayload of the first card.close
// targeting cardID, or fails the test if none.
func findClosePayload(t *testing.T, rec *emitv2.RecorderSink, cardID string) emitv2.ClosePayload {
	t.Helper()
	for _, ev := range rec.FilterByCard(cardID) {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			t.Fatalf("close event payload type = %T", ev.Payload)
		}
		return pl
	}
	t.Fatalf("no card.close found for %s", cardID)
	return emitv2.ClosePayload{}
}

// TestTranslator_ToolEnd_PassesSearchMetadataThrough is the regression
// guard for the web_search / tavily_search case: rich tool result
// metadata (urls, query, result_count, has_raw) must reach the wire
// via ToolPayload.Metadata, not be silently dropped.
func TestTranslator_ToolEnd_PassesSearchMetadataThrough(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_x")
	tr := NewTranslator(nil)

	// Open a tool first so EngineEventToolEnd has a card to close.
	tr.Translate(em, "sess_x", &types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolName:  "web_search",
		ToolUseID: "toolu_ws_1",
		ToolInput: `{"query":"vLLM 论文"}`,
	})

	tr.Translate(em, "sess_x", &types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolName:  "web_search",
		ToolUseID: "toolu_ws_1",
		ToolResult: &types.ToolResult{
			Content: "Search results for \"vLLM 论文\":\n\n--- Result 1 ---\nTitle: ...\nURL: https://...\n",
			Metadata: map[string]any{
				"render_hint":  "search",
				"query":        "vLLM 论文",
				"result_count": 5,
				"urls": []any{
					map[string]any{"url": "https://a.example", "title": "Result A"},
					map[string]any{"url": "https://b.example", "title": "Result B"},
				},
			},
		},
	})

	close := findClosePayload(t, rec, "toolu_ws_1")
	tp, ok := close.Inner.(emitv2.ToolPayload)
	if !ok {
		t.Fatalf("close.Inner is %T, want emitv2.ToolPayload", close.Inner)
	}

	// render_hint promoted to typed field.
	if tp.RenderHint != "search" {
		t.Errorf("RenderHint = %q, want search", tp.RenderHint)
	}
	// And stripped from passthrough Metadata (no duplication).
	if _, dup := tp.Metadata["render_hint"]; dup {
		t.Error("render_hint should be promoted out of Metadata, not duplicated")
	}
	// Search-specific fields preserved verbatim.
	if got, want := tp.Metadata["query"], "vLLM 论文"; got != want {
		t.Errorf("Metadata.query = %v, want %q (CRITICAL: regression of search metadata passthrough)", got, want)
	}
	if got, want := tp.Metadata["result_count"], 5; got != want {
		t.Errorf("Metadata.result_count = %v, want %d", got, want)
	}
	urls, ok := tp.Metadata["urls"].([]any)
	if !ok {
		t.Fatalf("Metadata.urls type = %T, want []any", tp.Metadata["urls"])
	}
	if len(urls) != 2 {
		t.Errorf("Metadata.urls len = %d, want 2", len(urls))
	}
}

// TestTranslator_ToolEnd_PassesTavilyHasRawThrough is a smaller
// counterpart for the tavily-specific has_raw flag.
func TestTranslator_ToolEnd_PassesTavilyHasRawThrough(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_t")
	tr := NewTranslator(nil)
	tr.Translate(em, "sess_t", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "tavily_search", ToolUseID: "toolu_tv",
		ToolInput: `{"query":"x"}`,
	})
	tr.Translate(em, "sess_t", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "tavily_search", ToolUseID: "toolu_tv",
		ToolResult: &types.ToolResult{
			Content: "...",
			Metadata: map[string]any{
				"render_hint": "search",
				"has_raw":     true,
				"query":       "x",
			},
		},
	})
	tp := findClosePayload(t, rec, "toolu_tv").Inner.(emitv2.ToolPayload)
	if tp.Metadata["has_raw"] != true {
		t.Errorf("has_raw lost: got %v", tp.Metadata["has_raw"])
	}
}

// TestTranslator_ToolEnd_PromotesAllKnownKeys verifies every promoted
// field gets stripped from Metadata so the wire never duplicates
// known keys.
func TestTranslator_ToolEnd_PromotesAllKnownKeys(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_p")
	tr := NewTranslator(nil)
	tr.Translate(em, "sess_p", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "bash", ToolUseID: "toolu_p",
		ToolInput: `{}`,
	})
	tr.Translate(em, "sess_p", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "bash", ToolUseID: "toolu_p",
		ToolResult: &types.ToolResult{
			Content: "out",
			Metadata: map[string]any{
				"render_hint": "terminal",
				"language":    "bash",
				"file_path":   "/tmp/x.sh",
				"exit_code":   0,
			},
		},
	})
	tp := findClosePayload(t, rec, "toolu_p").Inner.(emitv2.ToolPayload)
	if tp.RenderHint != "terminal" || tp.Language != "bash" || tp.FilePath != "/tmp/x.sh" {
		t.Errorf("typed promotion failed: %+v", tp)
	}
	want := map[string]any{"exit_code": 0}
	if !reflect.DeepEqual(tp.Metadata, want) {
		t.Errorf("Metadata after promotion = %+v, want %+v (only non-promoted keys remain)", tp.Metadata, want)
	}
}

// TestTranslator_ToolEnd_NoMetadataNoMap verifies that when a tool
// returns nil Metadata, ToolPayload.Metadata stays nil (so the wire
// frame omits the field rather than carrying an empty object).
func TestTranslator_ToolEnd_NoMetadataNoMap(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_n")
	tr := NewTranslator(nil)
	tr.Translate(em, "sess_n", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "X", ToolUseID: "tu_n",
	})
	tr.Translate(em, "sess_n", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "X", ToolUseID: "tu_n",
		ToolResult: &types.ToolResult{Content: "ok"},
	})
	tp := findClosePayload(t, rec, "tu_n").Inner.(emitv2.ToolPayload)
	if tp.Metadata != nil {
		t.Errorf("Metadata should be nil when tool has none, got %+v", tp.Metadata)
	}
}

// TestTranslator_PlanReview_PausesAgentCardWatchdog locks in the
// "prompt.user has no time limit" contract: while the user is reviewing
// a plan, the worker's CardAgent must not orphan-timeout no matter how
// long the user takes. ResolvePlanReview reverses the pause so the
// watchdog kicks back in once the response has been routed.
func TestTranslator_PlanReview_PausesAgentCardWatchdog(t *testing.T) {
	em, rec, tk := makeTrackedEmitter(t, "sess_pr")
	tr := NewTranslator(nil)

	// Stage the lineage the way a real plan-mode worker would: turn →
	// message → SubAgentStart opens an agent card.
	tr.Translate(em, "sess_pr", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_pr"})
	tr.Translate(em, "sess_pr", &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_worker",
		AgentName: "scheduler",
		AgentTask: "plan a thing",
	})

	// The worker is now tracked.
	if tk.OpenCount() < 3 {
		t.Fatalf("expected turn+msg+agent tracked, OpenCount=%d", tk.OpenCount())
	}

	// PlanCoordinator emits plan_proposed → translator pauses chain.
	tr.Translate(em, "sess_pr", &types.EngineEvent{
		Type:    types.EngineEventPlanProposed,
		AgentID: "agent_worker",
		PlanProposal: &types.PlanProposal{
			PlanID:  "pln_test1",
			AgentID: "agent_worker",
			Goal:    "anything",
			Steps: []types.ProposedStep{
				{ID: "s1", Description: "x", Prompt: "x"},
			},
		},
	})

	// Pull out the wire request_id the translator just minted.
	prompts := rec.FilterByType(emitv2.EventPromptUser)
	if len(prompts) != 1 {
		t.Fatalf("got %d prompt.user events, want 1", len(prompts))
	}
	pl := prompts[0].Payload.(emitv2.PromptUserPayload)
	if pl.Kind != "plan_review" {
		t.Fatalf("kind = %q, want plan_review", pl.Kind)
	}
	reqID := pl.RequestID

	// Force the watchdog through a long sweep: even way past the agent
	// card's 10-min orphan timeout there must be no synthetic close.
	// We can't advance the tracker's clock here (it owns its own), so
	// we directly assert the chain is paused via a follow-up Suspend
	// returning false (already paused).
	if tk.Suspend("agent_worker") {
		t.Error("agent card should already be paused after plan_proposed; Suspend returned true")
	}

	// User responds → ResolvePlanReview reverses the pause.
	if got := tr.ResolvePlanReview("sess_pr", reqID); got != "pln_test1" {
		t.Fatalf("ResolvePlanReview returned %q, want pln_test1", got)
	}

	// After resume, the card must be tracked-and-running again
	// (Suspend returns true because it's no longer paused).
	if !tk.Suspend("agent_worker") {
		t.Error("agent card should be unpaused after ResolvePlanReview; Suspend returned false")
	}
}

func TestTranslator_SubAgentClientToolCallUsesChildEmitter(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_browser_tool")
	tr := NewTranslator(nil)

	tr.Translate(em, "sess_browser_tool", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_browser_tool",
	})
	tr.Translate(em, "sess_browser_tool", &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_browser",
		AgentName: "browser-agent",
		AgentType: "sync",
	})
	tr.Translate(em, "sess_browser_tool", &types.EngineEvent{
		Type:           types.EngineEventToolCall,
		AgentID:        "agent_browser",
		ToolName:       "browser_session_create",
		ToolUseID:      "toolu_browser_session",
		ToolInput:      `{"visibility":"visible"}`,
		AwaitSessionID: "sess_browser_tool",
	})

	events := rec.FilterByCard("toolu_browser_session")
	if len(events) == 0 {
		t.Fatal("browser_session_create card was not emitted")
	}
	add := events[0]
	if add.Type != emitv2.EventCardAdd {
		t.Fatalf("event type = %s, want card.add", add.Type)
	}
	if add.Envelope.AgentID != "agent_browser" {
		t.Fatalf("tool card agent_id = %q, want sub-agent agent_browser", add.Envelope.AgentID)
	}
	if add.Envelope.ParentCardID != "agent_browser" {
		t.Fatalf("tool card parent = %q, want browser agent card", add.Envelope.ParentCardID)
	}
	payload, ok := add.Payload.(emitv2.ToolPayload)
	if !ok {
		t.Fatalf("payload type = %T, want ToolPayload", add.Payload)
	}
	if payload.Target != "client" {
		t.Fatalf("tool target = %q, want client", payload.Target)
	}
	if payload.Name != "browser_session_create" {
		t.Fatalf("tool name = %q", payload.Name)
	}
	if payload.AwaitSessionID != "sess_browser_tool" {
		t.Fatalf("await_session_id = %q, want sess_browser_tool", payload.AwaitSessionID)
	}
	if payload.Input["visibility"] != "visible" {
		t.Fatalf("tool input visibility = %v", payload.Input["visibility"])
	}
}

// TestTranslator_StepDispatchAttachesAgentUnderStep locks in the
// plan-mode topology: when SubAgentStart carries ParentStepID and the
// matching step card is open, the agent card must be parented under
// the step card. This is what gives the step's orphan watchdog a
// heartbeat path through the dispatched sub-agent's inner activity.
func TestTranslator_StepDispatchAttachesAgentUnderStep(t *testing.T) {
	em, rec, tk := makeTrackedEmitter(t, "sess_step")
	tr := NewTranslator(nil)

	// Open a plan card so step has somewhere natural to root, then
	// dispatch the step itself.
	tr.Translate(em, "sess_step", &types.EngineEvent{
		Type:    types.EngineEventPlanCreated,
		AgentID: "main",
		PlanEvent: &types.PlanEvent{
			PlanID:   "pln_a",
			Goal:     "x",
			Strategy: "sequential",
			Tasks:    []types.PlanTaskInfo{{TaskID: "s1"}},
		},
	})
	tr.Translate(em, "sess_step", &types.EngineEvent{
		Type: types.EngineEventStepDispatched,
		TaskDispatch: &types.TaskDispatch{
			TaskID: "s1", SubagentType: "researcher",
		},
	})

	// SubAgentStart carries ParentStepID — agent card must root under s1.
	tr.Translate(em, "sess_step", &types.EngineEvent{
		Type:         types.EngineEventSubAgentStart,
		AgentID:      "agent_42",
		AgentName:    "researcher",
		AgentTask:    "do research",
		ParentStepID: "s1",
	})

	// Find the agent card.add event and inspect its parent_card_id.
	var found bool
	for _, e := range rec.FilterByCard("agent_42") {
		if e.Type != emitv2.EventCardAdd {
			continue
		}
		if got := e.Envelope.ParentCardID; got != "s1" {
			t.Errorf("agent card.add parent_card_id = %q, want s1 (step)", got)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("no card.add for agent_42 emitted")
	}

	// And the heartbeat must propagate: child activity on the agent
	// resets the step card's deadline. Verify via Tracker — the parent
	// recorded for agent_42 should be the step card.
	if got := tk.ParentOf("agent_42"); got != "s1" {
		t.Errorf("Tracker parent of agent_42 = %q, want s1", got)
	}
}

// Backwards-compat: a SubAgentStart without ParentStepID (non-plan
// dispatch path) must still attach to the legacy parent (tool / message
// / turn). Without this, every direct sub-agent spawn from emma would
// orphan from its enclosing tool card.
func TestTranslator_SubAgentStart_FallsBackWhenNoStepID(t *testing.T) {
	em, rec, _ := makeTrackedEmitter(t, "sess_legacy")
	tr := NewTranslator(nil)

	tr.Translate(em, "sess_legacy", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_legacy", &types.EngineEvent{
		Type:      types.EngineEventSubAgentStart,
		AgentID:   "agent_x",
		AgentName: "n",
		AgentTask: "t",
	})

	for _, e := range rec.FilterByCard("agent_x") {
		if e.Type != emitv2.EventCardAdd {
			continue
		}
		if got := e.Envelope.ParentCardID; got == "" {
			t.Errorf("agent card.add parent_card_id is empty; expected fallback to message/turn")
		}
		return
	}
	t.Fatal("no card.add for agent_x emitted")
}

func TestTranslator_SubAgentParentPrefersExplicitToolCallID(t *testing.T) {
	s := &sessionState{
		tools:        map[string]string{"toolu_browser": "tool_card"},
		steps:        map[string]string{},
		subAgentCard: map[string]string{"parent_agent": "parent_agent_card"},
	}

	if got := parentForSubAgent(s, "parent_agent", "toolu_browser"); got != "tool_card" {
		t.Fatalf("parentForSubAgent = %q, want tool_card", got)
	}
}

// TestPromoteToolMetadata_OmitsKnownKeys exercises the helper directly.
func TestPromoteToolMetadata_OmitsKnownKeys(t *testing.T) {
	rh, lang, fp, rest := promoteToolMetadata(map[string]any{
		"render_hint": "search",
		"language":    "go",
		"file_path":   "/a",
		"extra":       42,
	})
	if rh != "search" || lang != "go" || fp != "/a" {
		t.Errorf("typed: rh=%q lang=%q fp=%q", rh, lang, fp)
	}
	if !reflect.DeepEqual(rest, map[string]any{"extra": 42}) {
		t.Errorf("rest = %+v", rest)
	}
}

func TestPromoteToolMetadata_NilWhenEmpty(t *testing.T) {
	if _, _, _, rest := promoteToolMetadata(nil); rest != nil {
		t.Errorf("nil input should yield nil rest, got %+v", rest)
	}
	if _, _, _, rest := promoteToolMetadata(map[string]any{
		"render_hint": "search",
	}); rest != nil {
		t.Errorf("only-known-keys input should yield nil rest, got %+v", rest)
	}
}

// TestTranslator_SystemNotice verifies that EngineEventSystemNotice is
// translated to a CardSystem card.add with the correct hint fields,
// severity, and SystemPayload content.
func TestTranslator_SystemNotice(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess-system-1")
	tr := NewTranslator(nil)

	tr.Translate(em, "sess-system-1", &types.EngineEvent{
		Type: types.EngineEventSystemNotice,
		SystemNotice: &types.SystemNotice{
			Topic:      "search_capability_gap",
			Title:      "搜索能力不可用",
			Summary:    "本次任务派到的 sub-agent (researcher) 依赖网络搜索。",
			ActionHint: "config.yaml: tools.web_search.enabled = true",
			Icon:       "warning",
		},
	})

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("want 1 emitted event, got %d", len(events))
	}
	ev := events[0]
	if ev.Envelope.CardKind != emitv2.CardSystem {
		t.Errorf("card_kind: got %q, want %q", ev.Envelope.CardKind, emitv2.CardSystem)
	}
	if ev.Hint == nil {
		t.Fatal("hint missing")
	}
	if ev.Hint.Title != "搜索能力不可用" {
		t.Errorf("hint title: got %q", ev.Hint.Title)
	}
	if ev.Hint.Icon != "warning" {
		t.Errorf("hint icon: got %q, want %q", ev.Hint.Icon, "warning")
	}
	// Note: AgentRole reflects the emitter binding (RolePersona in tests);
	// the registry default (RoleSystem) only applies when the emitter has
	// no bound role. We verify severity instead, which is translator-set.
	if ev.Envelope.Severity != emitv2.SeverityWarn {
		t.Errorf("severity: got %q, want %q", ev.Envelope.Severity, emitv2.SeverityWarn)
	}
	payload, ok := ev.Payload.(emitv2.SystemPayload)
	if !ok {
		t.Fatalf("payload type: got %T, want emitv2.SystemPayload", ev.Payload)
	}
	if payload.Summary == "" || payload.ActionHint == "" {
		t.Errorf("payload missing fields: %+v", payload)
	}
	if payload.Topic != "search_capability_gap" {
		t.Errorf("payload topic: got %q, want %q", payload.Topic, "search_capability_gap")
	}
}

// silence unused import when no wait references; needed to keep parity
// with the existing translator package layout.
var _ = context.Background
