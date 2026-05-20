package websocket

import (
	"math/rand"
	"testing"

	copypkg "harnessclaw-go/internal/copy"
	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/pkg/types"
)

// fixedPicker creates a deterministic CopyPicker for translator tests.
func fixedPicker(seed int64) *copypkg.CopyPicker {
	return copypkg.NewCopyPicker(func() *rand.Rand {
		return rand.New(rand.NewSource(seed))
	})
}

func TestTranslator_ToolPlanning_OpensCardEarly(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_plan")
	tr := NewTranslator(fixedPicker(1))

	// 现实流程下 MessageStart 先于 ToolPlanning
	tr.Translate(em, "sess_plan", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_1",
		Model:     "claude",
	})

	tr.Translate(em, "sess_plan", &types.EngineEvent{
		Type:      types.EngineEventToolPlanning,
		ToolUseID: "toolu_p1",
		ToolName:  "Bash",
	})

	// 应有一个 card.add(tool) 事件，cardKind=tool，phase=planning
	found := false
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardAdd {
			continue
		}
		if ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ToolPayload)
		if !ok {
			continue
		}
		if pl.Name == "Bash" && pl.Phase == emitv2.PhasePlanning {
			found = true
			if pl.PhaseHint == "" {
				t.Error("PhaseHint should be resolved by picker, got empty")
			}
		}
	}
	if !found {
		t.Error("expected card.add(tool, phase=planning, name=Bash)")
	}
}

func TestTranslator_ToolPlanning_Idempotent(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_idem")
	tr := NewTranslator(fixedPicker(1))

	tr.Translate(em, "sess_idem", &types.EngineEvent{
		Type: types.EngineEventMessageStart, MessageID: "msg_1",
	})
	// 同一 ToolUseID 来两次
	for i := 0; i < 2; i++ {
		tr.Translate(em, "sess_idem", &types.EngineEvent{
			Type:      types.EngineEventToolPlanning,
			ToolUseID: "toolu_dup",
			ToolName:  "Read",
		})
	}
	// 应该只有 1 个 card.add(tool)
	count := 0
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 tool card add, got %d", count)
	}
}

func TestTranslator_ToolPlanningProgress_SetsPhaseAndBytes(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_prog")
	tr := NewTranslator(fixedPicker(2))

	tr.Translate(em, "sess_prog", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_prog", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_w", ToolName: "Write",
	})

	tr.Translate(em, "sess_prog", &types.EngineEvent{
		Type: types.EngineEventToolPlanningProgress, ToolUseID: "toolu_w", ToolName: "Write", Bytes: 1234,
	})

	found := false
	// RecorderSink method: use the same method already used in TestTranslator_ToolPlanning_OpensCardEarly
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardSet {
			continue
		}
		patch, ok := ev.Payload.(map[string]any)
		if !ok {
			continue
		}
		if patch["phase"] == emitv2.PhasePlanningArgs && patch["phase_bytes"] == 1234 {
			found = true
			if hint, _ := patch["phase_hint"].(string); hint == "" {
				t.Error("phase_hint should be resolved")
			}
		}
	}
	if !found {
		t.Error("expected card.set with phase=planning_args + bytes=1234")
	}
}

func TestTranslator_ToolPlanningProgress_NoOpWhenCardMissing(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_orphan")
	tr := NewTranslator(fixedPicker(2))

	// 没有 ToolPlanning 直接发 Progress — 应该被忽略
	tr.Translate(em, "sess_orphan", &types.EngineEvent{
		Type: types.EngineEventToolPlanningProgress, ToolUseID: "toolu_x", Bytes: 500,
	})
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardSet {
			t.Errorf("unexpected card.set without preceding Planning: %+v", ev)
		}
	}
}

func TestTranslator_ToolQueued_SetsPhaseQueued(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_q")
	tr := NewTranslator(fixedPicker(3))

	tr.Translate(em, "sess_q", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_q", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_q1", ToolName: "Bash",
	})
	tr.Translate(em, "sess_q", &types.EngineEvent{
		Type: types.EngineEventToolQueued, ToolUseID: "toolu_q1", ToolName: "Bash",
	})

	found := false
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardSet {
			continue
		}
		patch, _ := ev.Payload.(map[string]any)
		if patch["phase"] == emitv2.PhaseQueued {
			found = true
		}
	}
	if !found {
		t.Error("expected card.set with phase=queued")
	}
}

func TestTranslator_Retract_ClosesPlanningCards(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_retract")
	tr := NewTranslator(fixedPicker(4))

	tr.Translate(em, "sess_retract", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_a", ToolName: "Bash",
	})
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_b", ToolName: "Read",
	})

	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanningRetract,
	})

	closedCount := 0
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		if ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		pl, _ := ev.Payload.(emitv2.ClosePayload)
		if pl.Status == emitv2.StatusCancelled {
			closedCount++
		}
	}
	if closedCount != 2 {
		t.Errorf("expected 2 cancelled closes, got %d", closedCount)
	}
}

func TestTranslator_Retract_DoesNotCloseUpgradedTools(t *testing.T) {
	// Un-skip after Task 17 wires the ToolStart idempotent upgrade that deletes from s.toolsFromPlanning.
	t.Skip("requires Task 17 idempotent ToolStart upgrade")

	em, rec := makeRecorderEmitter(t, "sess_upg")
	tr := NewTranslator(fixedPicker(4))

	tr.Translate(em, "sess_upg", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_upg", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_a", ToolName: "Bash",
	})
	// ToolStart 把 toolu_a 转正
	tr.Translate(em, "sess_upg", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolUseID: "toolu_a", ToolName: "Bash", ToolInput: `{"command":"ls"}`,
	})

	// 现在发 Retract — 不应关掉 toolu_a
	tr.Translate(em, "sess_upg", &types.EngineEvent{Type: types.EngineEventToolPlanningRetract})

	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardClose && ev.Envelope.CardKind == emitv2.CardTool {
			t.Errorf("upgraded tool should not be closed by retract: %+v", ev)
		}
	}
}
