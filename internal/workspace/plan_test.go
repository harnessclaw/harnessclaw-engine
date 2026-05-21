package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPlan_RoundTrip(t *testing.T) {
	p := &Plan{
		SessionID: "sess_a",
		CreatedAt: time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
		Tasks: map[string]*Task{
			"t_001": {
				Title:   "调研",
				Agent:   "researcher",
				Status:  StatusDone,
				Attempt: 1,
			},
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Plan
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tasks["t_001"].Status != StatusDone {
		t.Errorf("status round-trip lost")
	}
}

func TestPlan_StatusEnumRejected(t *testing.T) {
	p := &Plan{
		SessionID: "sess_a",
		Tasks: map[string]*Task{
			"t_001": {Title: "x", Agent: "y", Status: "weird"},
		},
	}
	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Errorf("expected status enum error, got %v", err)
	}
}

func TestPlan_DependsOnRefMissing(t *testing.T) {
	p := &Plan{
		SessionID: "sess_a",
		Tasks: map[string]*Task{
			"t_001": {Title: "x", Agent: "y", Status: StatusPending, DependsOn: []string{"t_missing"}},
		},
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "t_missing") {
		t.Errorf("expected dangling depends_on error, got %v", err)
	}
}

func TestPlan_FrozenImmutability(t *testing.T) {
	old := &Plan{
		SessionID: "sess_a",
		Tasks:     map[string]*Task{"t_001": {Title: "x", Agent: "y", Status: StatusDone, Frozen: true}},
	}
	next := &Plan{
		SessionID: "sess_a",
		Tasks:     map[string]*Task{"t_001": {Title: "x", Agent: "y", Status: StatusDone, Frozen: false}},
	}
	if err := next.ValidateTransitionFrom(old); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Errorf("expected frozen-irreversible error, got %v", err)
	}
}

func TestPlan_StatusDoneRequiresSummaryRef(t *testing.T) {
	p := &Plan{
		SessionID: "sess_a",
		Tasks: map[string]*Task{
			"t_001": {Title: "x", Agent: "y", Status: StatusDone},
		},
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "summary_ref") {
		t.Errorf("expected summary_ref error, got %v", err)
	}
}
