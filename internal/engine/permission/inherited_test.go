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
	ic := NewInheritedChecker([]string{"bash", "edit"})
	r := ic.Check(context.Background(), "bash", nil, false)
	if r.Decision != Allow {
		t.Errorf("expected Allow for approved tool, got %s", r.Decision)
	}
	if r.Reason != ReasonRule {
		t.Errorf("expected reason %s, got %s", ReasonRule, r.Reason)
	}
}

func TestInheritedChecker_UnapprovedToolAsks(t *testing.T) {
	ic := NewInheritedChecker([]string{"bash"})
	r := ic.Check(context.Background(), "edit", nil, false)
	if r.Decision != Ask {
		t.Errorf("expected Ask for unapproved tool, got %s", r.Decision)
	}
	if r.Reason != ReasonDefault {
		t.Errorf("expected reason %s, got %s", ReasonDefault, r.Reason)
	}
}

func TestInheritedChecker_DynamicApproval(t *testing.T) {
	ic := NewInheritedChecker(nil)
	r := ic.Check(context.Background(), "bash", nil, false)
	if r.Decision != Ask {
		t.Errorf("expected Ask before approval, got %s", r.Decision)
	}
	ic.Approve("bash")
	r = ic.Check(context.Background(), "bash", nil, false)
	if r.Decision != Allow {
		t.Errorf("expected Allow after approval, got %s", r.Decision)
	}
}

func TestInheritedChecker_EmptyApprovedList(t *testing.T) {
	ic := NewInheritedChecker([]string{})
	r := ic.Check(context.Background(), "bash", nil, false)
	if r.Decision != Ask {
		t.Errorf("expected Ask with empty approved list, got %s", r.Decision)
	}
}

func TestInheritedChecker_MultipleApprovals(t *testing.T) {
	ic := NewInheritedChecker([]string{"bash"})
	ic.Approve("edit")
	ic.Approve("write")

	for _, tool := range []string{"bash", "edit", "write"} {
		r := ic.Check(context.Background(), tool, nil, false)
		if r.Decision != Allow {
			t.Errorf("expected Allow for %s after approval, got %s", tool, r.Decision)
		}
	}
}

func TestInheritedChecker_ImplementsCheckerInterface(t *testing.T) {
	var _ Checker = (*InheritedChecker)(nil)
}
