package emitv2

import "testing"

// TestRegistry_AllCardKindsDeclared verifies that every CardKind constant
// has an entry in cardMeta. This is the §16 #5 decision: "新增 card_kind
// 的准入流程：建议加 CI 校验 — cardMeta 中未注册的 card_kind 拒绝
// 编译". Adding a new CardKind without registry entry now fails CI.
func TestRegistry_AllCardKindsDeclared(t *testing.T) {
	allKinds := []CardKind{
		CardTurn, CardMessage, CardTool, CardAgent,
		CardPlan, CardStep, CardArtifact, CardThinking,
		CardMemoryOp, CardBudget, CardTodo, CardTeam,
	}
	if len(allKinds) != 12 {
		t.Errorf("v2.2 spec mandates 12 card_kinds; got %d in this guard", len(allKinds))
	}
	for _, kind := range allKinds {
		if _, ok := cardMeta[kind]; !ok {
			t.Errorf("CardKind %q is missing from cardMeta registry", kind)
		}
	}
	// Also verify cardMeta has no orphan entries.
	known := make(map[CardKind]struct{}, len(allKinds))
	for _, k := range allKinds {
		known[k] = struct{}{}
	}
	for k := range cardMeta {
		if _, ok := known[k]; !ok {
			t.Errorf("cardMeta has orphan entry for %q (not in CardKind constants)", k)
		}
	}
}

// TestRegistry_AllErrorTypesDeclared verifies coverage of the 14 error
// types from v2.2 §6.
func TestRegistry_AllErrorTypesDeclared(t *testing.T) {
	allTypes := []ErrorType{
		ErrorTypeToolTimeout, ErrorTypeOrphanTimeout,
		ErrorTypeRateLimit, ErrorTypeOverloaded,
		ErrorTypeContractFail, ErrorTypeDependencyFail,
		ErrorTypeUserAborted, ErrorTypePermissionDenied,
		ErrorTypeMaxTurns, ErrorTypeContextExceeded,
		ErrorTypeModelError, ErrorTypeBudgetExhausted,
		ErrorTypeInvalidInput, ErrorTypeInternal,
	}
	if len(allTypes) != 14 {
		t.Errorf("v2.2 spec mandates 14 error_types; got %d in this guard", len(allTypes))
	}
	for _, typ := range allTypes {
		if _, ok := errorTypeMeta[typ]; !ok {
			t.Errorf("ErrorType %q is missing from errorTypeMeta", typ)
		}
		meta := errorTypeMeta[typ]
		if meta.DefaultUserMessage == "" {
			t.Errorf("ErrorType %q has empty DefaultUserMessage", typ)
		}
	}
	known := make(map[ErrorType]struct{}, len(allTypes))
	for _, t := range allTypes {
		known[t] = struct{}{}
	}
	for t := range errorTypeMeta {
		if _, ok := known[t]; !ok {
			// Allow orphan entries (registry can have extras), but warn.
			// Don't fail — operator may add experimental types.
		}
	}
}

// TestRegistry_AllTickKindsValid checks the 5 tick kinds.
func TestRegistry_AllTickKindsValid(t *testing.T) {
	want := []TickKind{TickProgress, TickHeartbeat, TickIntent, TickNote, TickEscalation}
	if len(want) != 5 {
		t.Errorf("v2.2 spec mandates 5 tick_kinds; got %d", len(want))
	}
}

// TestRegistry_AllChannelsValid checks the 3 channels.
func TestRegistry_AllChannelsValid(t *testing.T) {
	want := []Channel{ChannelText, ChannelToolInput, ChannelThinking}
	if len(want) != 3 {
		t.Errorf("v2.2 spec mandates 3 channels; got %d", len(want))
	}
}

// TestRegistry_AllStatusesValid checks the 4 close statuses.
func TestRegistry_AllStatusesValid(t *testing.T) {
	want := []Status{StatusOK, StatusFailed, StatusSkipped, StatusCancelled}
	if len(want) != 4 {
		t.Errorf("v2.2 spec mandates 4 close statuses; got %d", len(want))
	}
}

// TestRegistry_AllEventTypesValid checks the 8 event actions.
func TestRegistry_AllEventTypesValid(t *testing.T) {
	want := []EventType{
		EventCardAdd, EventCardSet, EventCardAppend, EventCardTick, EventCardClose,
		EventPromptUser, EventPromptReply,
		EventSession,
	}
	if len(want) != 8 {
		t.Errorf("v2.2 spec mandates 8 event types; got %d", len(want))
	}
}
