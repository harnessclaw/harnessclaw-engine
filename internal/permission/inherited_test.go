package permission

import (
	"context"
	"testing"
)

func TestInheritedChecker_ReadOnlyAlwaysAllowed(t *testing.T) {
	ic := NewInheritedChecker(nil)
	r := ic.Check(context.Background(), "SomeWriteTool", nil, true)
	if r.Decision != Allow {
		t.Errorf("expected Allow for read-only, got %s", r.Decision)
	}
	if r.Reason != ReasonReadOnly {
		t.Errorf("expected reason %s, got %s", ReasonReadOnly, r.Reason)
	}
}

func TestInheritedChecker_ApprovedToolAllowed(t *testing.T) {
	ic := NewInheritedChecker([]string{"Bash", "Edit"})
	r := ic.Check(context.Background(), "Bash", nil, false)
	if r.Decision != Allow {
		t.Errorf("expected Allow for approved tool, got %s", r.Decision)
	}
	if r.Reason != ReasonRule {
		t.Errorf("expected reason %s, got %s", ReasonRule, r.Reason)
	}
}

func TestInheritedChecker_UnapprovedToolDenied(t *testing.T) {
	ic := NewInheritedChecker([]string{"Bash"})
	r := ic.Check(context.Background(), "Edit", nil, false)
	if r.Decision != Deny {
		t.Errorf("expected Deny for unapproved tool, got %s", r.Decision)
	}
	if r.Reason != ReasonDefault {
		t.Errorf("expected reason %s, got %s", ReasonDefault, r.Reason)
	}
}

func TestInheritedChecker_DynamicApproval(t *testing.T) {
	ic := NewInheritedChecker(nil)
	r := ic.Check(context.Background(), "Bash", nil, false)
	if r.Decision != Deny {
		t.Errorf("expected Deny before approval, got %s", r.Decision)
	}
	ic.Approve("Bash")
	r = ic.Check(context.Background(), "Bash", nil, false)
	if r.Decision != Allow {
		t.Errorf("expected Allow after approval, got %s", r.Decision)
	}
}

func TestInheritedChecker_EmptyApprovedList(t *testing.T) {
	ic := NewInheritedChecker([]string{})
	r := ic.Check(context.Background(), "Bash", nil, false)
	if r.Decision != Deny {
		t.Errorf("expected Deny with empty approved list, got %s", r.Decision)
	}
}

func TestInheritedChecker_MultipleApprovals(t *testing.T) {
	ic := NewInheritedChecker([]string{"Bash"})
	ic.Approve("Edit")
	ic.Approve("Write")

	for _, tool := range []string{"Bash", "Edit", "Write"} {
		r := ic.Check(context.Background(), tool, nil, false)
		if r.Decision != Allow {
			t.Errorf("expected Allow for %s after approval, got %s", tool, r.Decision)
		}
	}
}

func TestInheritedChecker_ImplementsCheckerInterface(t *testing.T) {
	var _ Checker = (*InheritedChecker)(nil)
}
