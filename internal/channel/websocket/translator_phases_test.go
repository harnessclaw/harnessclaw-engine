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
