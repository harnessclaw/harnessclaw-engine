package engine

import (
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestEscalationContext_IsEmpty(t *testing.T) {
	if !((*EscalationContext)(nil)).IsEmpty() {
		t.Error("nil receiver should be empty")
	}
	if !(&EscalationContext{}).IsEmpty() {
		t.Error("zero-value should be empty")
	}
	withAttempt := &EscalationContext{PriorAttempts: []PriorAttempt{{Skill: "writer"}}}
	if withAttempt.IsEmpty() {
		t.Error("escalation with prior attempts should not be empty")
	}
	withArt := &EscalationContext{PriorArtifacts: []types.ArtifactRef{{ArtifactID: "art_x"}}}
	if withArt.IsEmpty() {
		t.Error("escalation with prior artifacts should not be empty")
	}
	withFail := &EscalationContext{Failures: []string{"x"}}
	if withFail.IsEmpty() {
		t.Error("escalation with failures should not be empty")
	}
}

func TestEscalationContext_FormatForLog(t *testing.T) {
	ec := &EscalationContext{
		FromMode:       CoordinatorModeReAct,
		Reason:         "react timed out after 3 contract failures",
		PriorAttempts:  []PriorAttempt{{Skill: "writer"}, {Skill: "researcher"}},
		PriorArtifacts: []types.ArtifactRef{{ArtifactID: "art_a"}},
	}
	got := ec.FormatForLog()
	for _, want := range []string{"from=react", "attempts=2", "artifacts=1", "react timed out"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatForLog missing %q in: %q", want, got)
		}
	}
}

func TestEscalationContext_FormatForLogTruncatesLongReason(t *testing.T) {
	long := strings.Repeat("x", 200)
	ec := &EscalationContext{Reason: long, PriorAttempts: []PriorAttempt{{}}}
	got := ec.FormatForLog()
	if strings.Contains(got, long) {
		t.Errorf("long reason should be truncated; got %d chars", len(got))
	}
	if !strings.Contains(got, "...") {
		t.Errorf("truncation marker missing")
	}
}

func TestEscalationContext_FormatForLog_EmptyShortCircuit(t *testing.T) {
	if got := (&EscalationContext{}).FormatForLog(); got != "(empty)" {
		t.Errorf("empty escalation should format as (empty); got %q", got)
	}
}
