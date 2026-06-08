package spec_test

import (
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/spec"
)

func TestEscalationInfo_IsEmpty(t *testing.T) {
	var ei *spec.EscalationInfo
	if !ei.IsEmpty() {
		t.Fatal("nil EscalationInfo should be empty")
	}
	ei = &spec.EscalationInfo{}
	if !ei.IsEmpty() {
		t.Fatal("zero EscalationInfo should be empty")
	}
	ei = &spec.EscalationInfo{Reason: "too complex"}
	if ei.IsEmpty() {
		t.Fatal("EscalationInfo with Reason should not be empty")
	}
}

func TestTaskSpec_EscalationInfoField(t *testing.T) {
	sp := spec.TaskSpec{
		Goal: "test",
		Escalation: &spec.EscalationInfo{
			FromKind:    "react",
			Reason:      "failed to complete in react mode",
			Failures:    []string{"tool timed out"},
			EscalatedAt: time.Now(),
		},
	}
	if sp.Escalation.IsEmpty() {
		t.Fatal("expected non-empty escalation")
	}
}
