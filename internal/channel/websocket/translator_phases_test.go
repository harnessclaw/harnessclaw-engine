package websocket

import (
	"math/rand"
	"testing"

	emitv2 "harnessclaw-go/internal/channel/emit/v2"
	"harnessclaw-go/internal/channel/websocket/internal/toolphrase"
	"harnessclaw-go/pkg/types"
)

// fixedPicker creates a deterministic Picker for translator tests.
func fixedPicker(seed int64) *toolphrase.Picker {
	return toolphrase.NewPicker(func() *rand.Rand {
		return rand.New(rand.NewSource(seed))
	})
}

func TestTranslator_ToolPlanning_NoWireEventUntilQueued(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_plan")
	tr := NewTranslator(fixedPicker(1))

	// 现实流程：MessageStart → ToolPlanning（流式期间）
	tr.Translate(em, "sess_plan", &types.EngineEvent{
		Type:      types.EngineEventMessageStart,
		MessageID: "msg_1",
		Model:     "claude",
	})
	tr.Translate(em, "sess_plan", &types.EngineEvent{
		Type:      types.EngineEventToolPlanning,
		ToolUseID: "toolu_p1",
		ToolName:  "bash",
	})

	// ToolPlanning 阶段不应向客户端发送任何工具卡事件
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			t.Errorf("ToolPlanning should not emit card.add; got %+v", ev)
		}
	}

	// ToolQueued（文字 replay 之后）才开卡
	tr.Translate(em, "sess_plan", &types.EngineEvent{
		Type:      types.EngineEventToolQueued,
		ToolUseID: "toolu_p1",
		ToolName:  "bash",
	})

	found := false
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardAdd || ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ToolPayload)
		if !ok {
			continue
		}
		if pl.Name == "bash" && pl.Phase == emitv2.PhaseQueued {
			found = true
		}
	}
	if !found {
		t.Error("expected card.add(tool, phase=queued, name=Bash) after ToolQueued")
	}
}

func TestTranslator_ToolPlanning_Idempotent(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_idem")
	tr := NewTranslator(fixedPicker(1))

	tr.Translate(em, "sess_idem", &types.EngineEvent{
		Type: types.EngineEventMessageStart, MessageID: "msg_1",
	})
	// 同一 ToolUseID 来两次 — 仍只应跟踪一次
	for i := 0; i < 2; i++ {
		tr.Translate(em, "sess_idem", &types.EngineEvent{
			Type:      types.EngineEventToolPlanning,
			ToolUseID: "toolu_dup",
			ToolName:  "read",
		})
	}
	// ToolPlanning 阶段不开卡
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			t.Error("ToolPlanning should not open tool card")
		}
	}
	// ToolQueued 触发后应有且仅有 1 个 card.add(tool)
	tr.Translate(em, "sess_idem", &types.EngineEvent{
		Type:      types.EngineEventToolQueued,
		ToolUseID: "toolu_dup",
		ToolName:  "read",
	})
	count := 0
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 tool card add after ToolQueued, got %d", count)
	}
}

func TestTranslator_ToolPlanningProgress_NoOpDuringPlanning(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_prog")
	tr := NewTranslator(fixedPicker(2))

	// 流式期间 ToolPlanning → ToolPlanningProgress：
	// 工具卡尚未发到客户端，Progress 应该是无操作。
	tr.Translate(em, "sess_prog", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_prog", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_w", ToolName: "write",
	})
	tr.Translate(em, "sess_prog", &types.EngineEvent{
		Type: types.EngineEventToolPlanningProgress, ToolUseID: "toolu_w", ToolName: "write", Bytes: 1234,
	})

	// 不应有任何 card.set（planning_args 或其他）
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardSet && ev.Envelope.CardKind == emitv2.CardTool {
			patch, _ := ev.Payload.(map[string]any)
			t.Errorf("ToolPlanningProgress should be no-op before ToolQueued; got card.set %v", patch)
		}
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

func TestTranslator_ToolQueued_OpensCard(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_q")
	tr := NewTranslator(fixedPicker(3))

	tr.Translate(em, "sess_q", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_q", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_q1", ToolName: "bash",
	})
	// 文字 replay（简化为无文字的纯工具轮次）
	tr.Translate(em, "sess_q", &types.EngineEvent{
		Type: types.EngineEventToolQueued, ToolUseID: "toolu_q1", ToolName: "bash",
	})

	// ToolQueued 应发 card.add(tool, phase=queued)，而非 card.set
	found := false
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardAdd || ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ToolPayload)
		if !ok {
			continue
		}
		if pl.Phase == emitv2.PhaseQueued && pl.Name == "bash" {
			found = true
		}
	}
	if !found {
		t.Error("expected card.add(tool, phase=queued) from ToolQueued")
	}
}

func TestTranslator_Retract_ClearsStateWithoutClosingCards(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_retract")
	tr := NewTranslator(fixedPicker(4))

	// planning 阶段工具卡从未 card.add 到客户端，retract 只需清理内部状态。
	tr.Translate(em, "sess_retract", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_a", ToolName: "bash",
	})
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_b", ToolName: "read",
	})
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolPlanningRetract,
	})

	// 不应有任何工具卡 close 事件（卡从未被 add）
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardClose && ev.Envelope.CardKind == emitv2.CardTool {
			t.Errorf("retract should not emit card.close for never-added planning cards: %+v", ev)
		}
	}

	// 验证内部状态已清理：ToolQueued 发送 a/b 不应开卡（已被 retract 清除）
	tr.Translate(em, "sess_retract", &types.EngineEvent{
		Type: types.EngineEventToolQueued, ToolUseID: "toolu_a", ToolName: "bash",
	})
	addCount := 0
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			addCount++
		}
	}
	// retract 清除了 toolu_a，ToolQueued 找不到 toolsFromPlanning 记录，
	// 走 "未见过" 路径新建 ID 并开卡 — 仍应有 1 个 add（新 attempt 的工具）
	if addCount != 1 {
		t.Errorf("after retract, ToolQueued for new attempt should open 1 card, got %d", addCount)
	}
}

func TestTranslator_Retract_DoesNotCloseUpgradedTools(t *testing.T) {

	em, rec := makeRecorderEmitter(t, "sess_upg")
	tr := NewTranslator(fixedPicker(4))

	tr.Translate(em, "sess_upg", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_upg", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_a", ToolName: "bash",
	})
	// ToolStart 把 toolu_a 转正
	tr.Translate(em, "sess_upg", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolUseID: "toolu_a", ToolName: "bash", ToolInput: `{"command":"ls"}`,
	})

	// 现在发 Retract — 不应关掉 toolu_a
	tr.Translate(em, "sess_upg", &types.EngineEvent{Type: types.EngineEventToolPlanningRetract})

	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardClose && ev.Envelope.CardKind == emitv2.CardTool {
			t.Errorf("upgraded tool should not be closed by retract: %+v", ev)
		}
	}
}

func TestTranslator_NextRoundThinking_PreOpensMessageCard(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_m4")
	tr := NewTranslator(fixedPicker(5))

	// 走完一轮：MessageStart → ToolStart → ToolEnd → MessageStop
	tr.Translate(em, "sess_m4", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_m4", &types.EngineEvent{Type: types.EngineEventToolStart, ToolUseID: "toolu_1", ToolName: "read", ToolInput: `{"path":"/x"}`})
	tr.Translate(em, "sess_m4", &types.EngineEvent{Type: types.EngineEventToolEnd, ToolUseID: "toolu_1", ToolName: "read", ToolResult: &types.ToolResult{Content: "ok"}})
	tr.Translate(em, "sess_m4", &types.EngineEvent{Type: types.EngineEventMessageStop})

	// 现在发 NextRoundThinking — 应该开新 message card 带 hint
	tr.Translate(em, "sess_m4", &types.EngineEvent{Type: types.EngineEventNextRoundThinking})

	// 2 个 card.add(message)；第二个带 Hint.Summary
	msgAdds := 0
	var lastSummary string
	for _, ev := range rec.Events() {
		if ev.Type != emitv2.EventCardAdd {
			continue
		}
		if ev.Envelope.CardKind != emitv2.CardMessage {
			continue
		}
		msgAdds++
		if ev.Hint != nil {
			lastSummary = ev.Hint.Summary
		}
	}
	if msgAdds != 2 {
		t.Errorf("expected 2 message adds, got %d", msgAdds)
	}
	if lastSummary == "" {
		t.Error("expected Hint.Summary on the M4 message card")
	}
}

func TestTranslator_ToolStart_UpgradesPlanningCard(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_up")
	tr := NewTranslator(fixedPicker(6))

	// 真实事件顺序：ToolPlanning（流式）→ ToolQueued（文字 replay 后）→ ToolStart（执行）
	tr.Translate(em, "sess_up", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_up", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_u", ToolName: "bash",
	})
	tr.Translate(em, "sess_up", &types.EngineEvent{
		Type: types.EngineEventToolQueued, ToolUseID: "toolu_u", ToolName: "bash",
	})
	tr.Translate(em, "sess_up", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolUseID: "toolu_u", ToolName: "bash", ToolInput: `{"command":"ls"}`,
	})

	// ToolQueued 开卡（phase=queued），ToolStart 升级（phase=executing）
	// 总计 1 个 card.add(tool)，有 card.set 带 phase=executing
	adds := 0
	sawExecuting := false
	for _, ev := range rec.Events() {
		if ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		if ev.Type == emitv2.EventCardAdd {
			adds++
		}
		if ev.Type == emitv2.EventCardSet {
			patch, _ := ev.Payload.(map[string]any)
			if patch["phase"] == emitv2.PhaseExecuting {
				sawExecuting = true
			}
		}
	}
	if adds != 1 {
		t.Errorf("expected 1 tool add (from ToolQueued), got %d", adds)
	}
	if !sawExecuting {
		t.Error("expected card.set with phase=executing from ToolStart")
	}
}

func TestTranslator_ToolStart_OpensFreshCardIfNoPlanning(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_fresh")
	tr := NewTranslator(fixedPicker(6))

	tr.Translate(em, "sess_fresh", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_fresh", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolUseID: "toolu_f", ToolName: "read", ToolInput: `{"path":"/x"}`,
	})

	adds := 0
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardTool {
			adds++
		}
	}
	if adds != 1 {
		t.Errorf("expected 1 tool add (no planning), got %d", adds)
	}
}

func TestTranslator_MessageStart_AfterNextRound_DoesNotDoubleAdd(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_m4b")
	tr := NewTranslator(fixedPicker(5))

	tr.Translate(em, "sess_m4b", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_m4b", &types.EngineEvent{Type: types.EngineEventMessageStop})

	// 先 NextRoundThinking 预开
	tr.Translate(em, "sess_m4b", &types.EngineEvent{Type: types.EngineEventNextRoundThinking})

	// 然后下一轮 MessageStart 到达 — 不应再开
	tr.Translate(em, "sess_m4b", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_2", Model: "claude"})

	// 应该 2 个 message add，不是 3 个
	msgAdds := 0
	for _, ev := range rec.Events() {
		if ev.Type == emitv2.EventCardAdd && ev.Envelope.CardKind == emitv2.CardMessage {
			msgAdds++
		}
	}
	if msgAdds != 2 {
		t.Errorf("expected 2 message adds, got %d", msgAdds)
	}
}

func TestTranslator_PermissionRequest_SetsPhaseWait(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_perm")
	tr := NewTranslator(fixedPicker(7))

	tr.Translate(em, "sess_perm", &types.EngineEvent{Type: types.EngineEventMessageStart, MessageID: "msg_1"})
	tr.Translate(em, "sess_perm", &types.EngineEvent{
		Type: types.EngineEventToolPlanning, ToolUseID: "toolu_pp", ToolName: "bash",
	})
	tr.Translate(em, "sess_perm", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolUseID: "toolu_pp", ToolName: "bash", ToolInput: `{}`,
	})

	tr.Translate(em, "sess_perm", &types.EngineEvent{
		Type: types.EngineEventPermissionRequest,
		PermissionRequest: &types.PermissionRequest{
			RequestID: "perm_x",
			ToolUseID: "toolu_pp",
			ToolName:  "bash",
			Message:   "Allow git?",
			Options: []types.PermissionOption{
				{Label: "Allow once", Scope: types.PermissionScopeOnce, Allow: true},
			},
		},
	})

	sawWait := false
	for _, ev := range rec.Events() {
		if ev.Envelope.CardKind != emitv2.CardTool {
			continue
		}
		if ev.Type != emitv2.EventCardSet {
			continue
		}
		patch, _ := ev.Payload.(map[string]any)
		if patch["phase"] == emitv2.PhasePermissionWait {
			sawWait = true
		}
	}
	if !sawWait {
		t.Error("expected card.set with phase=permission_wait")
	}
}
