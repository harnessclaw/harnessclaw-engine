package engine

import (
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestShouldEscalate_CompletedCleanResult(t *testing.T) {
	r := subAgentLoopResult{Terminal: types.Terminal{Reason: types.TerminalCompleted}}
	if shouldEscalate(r) {
		t.Error("clean completion should NOT escalate")
	}
}

func TestShouldEscalate_CompletedWithContractFailures(t *testing.T) {
	r := subAgentLoopResult{
		Terminal:         types.Terminal{Reason: types.TerminalCompleted},
		ContractFailures: []string{"missing required role"},
	}
	if !shouldEscalate(r) {
		t.Error("completion with contract failures should escalate")
	}
}

func TestShouldEscalate_UserAbortDoesNotEscalate(t *testing.T) {
	for _, reason := range []types.TerminalReason{
		types.TerminalAbortedStreaming,
		types.TerminalAbortedTools,
	} {
		r := subAgentLoopResult{Terminal: types.Terminal{Reason: reason}}
		if shouldEscalate(r) {
			t.Errorf("user abort %q should NOT escalate", reason)
		}
	}
}

func TestShouldEscalate_PromptTooLongDoesNotEscalate(t *testing.T) {
	r := subAgentLoopResult{
		Terminal: types.Terminal{Reason: types.TerminalPromptTooLong},
	}
	if shouldEscalate(r) {
		t.Error("prompt-too-long should NOT escalate (plan won't help)")
	}
}

func TestShouldEscalate_ModelErrorEscalates(t *testing.T) {
	for _, reason := range []types.TerminalReason{
		types.TerminalModelError,
		types.TerminalBlockingLimit,
		types.TerminalMaxTurns,
	} {
		r := subAgentLoopResult{Terminal: types.Terminal{Reason: reason}}
		if !shouldEscalate(r) {
			t.Errorf("recoverable failure %q SHOULD escalate", reason)
		}
	}
}

func TestBuildReActEscalation_PreservesEvidence(t *testing.T) {
	r := subAgentLoopResult{
		Terminal: types.Terminal{
			Reason:  types.TerminalModelError,
			Message: "stream error",
		},
		ContractFailures:   []string{"role draft missing"},
		SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_partial"}},
	}
	budget := BudgetSnapshot{TokensUsed: 5000}

	ec := buildReActEscalation(r, budget)
	if ec == nil {
		t.Fatal("escalation should not be nil")
	}
	if ec.FromMode != CoordinatorModeReAct {
		t.Errorf("FromMode should be react; got %q", ec.FromMode)
	}
	if len(ec.PriorArtifacts) != 1 || ec.PriorArtifacts[0].ArtifactID != "art_partial" {
		t.Errorf("prior artifacts not propagated; got %v", ec.PriorArtifacts)
	}
	if len(ec.Failures) != 1 {
		t.Errorf("failures not propagated; got %v", ec.Failures)
	}
	if ec.BudgetSpent.TokensUsed != 5000 {
		t.Errorf("budget snapshot not propagated; got %+v", ec.BudgetSpent)
	}
	if ec.Reason == "" {
		t.Error("reason should be set")
	}
	if ec.EscalatedAt.IsZero() {
		t.Error("EscalatedAt should be stamped")
	}
}

func TestBuildReActEscalation_FallsBackOnEmptyMessage(t *testing.T) {
	r := subAgentLoopResult{
		Terminal: types.Terminal{Reason: types.TerminalModelError},
	}
	ec := buildReActEscalation(r, BudgetSnapshot{})
	if ec.Reason == "" {
		t.Error("empty Terminal.Message should still produce a non-empty reason")
	}
}

func TestNewReActCoordinator_RespectsAllowEscalationFlag(t *testing.T) {
	deps := &SharedDeps{}
	c := NewReActCoordinator(deps, false)
	if c.allowEscalation {
		t.Error("flag not propagated")
	}
	c2 := NewReActCoordinator(deps, true)
	if !c2.allowEscalation {
		t.Error("flag not propagated")
	}
}
