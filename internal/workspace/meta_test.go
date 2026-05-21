package workspace

import (
	"strings"
	"testing"
)

func TestMeta_Valid(t *testing.T) {
	m := &Meta{
		TaskID:  "t_001",
		Agent:   "researcher",
		Status:  StatusDone,
		Summary: "对比 5 家产品",
		Outputs: []Output{{Path: "tasks/t_001/output.md", Type: "markdown", Bytes: 100}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestMeta_SummaryEmpty(t *testing.T) {
	m := &Meta{TaskID: "t_001", Agent: "x", Status: StatusDone, Summary: ""}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Errorf("expected summary error, got %v", err)
	}
}

func TestMeta_SummaryTooLong(t *testing.T) {
	m := &Meta{
		TaskID: "t_001", Agent: "x", Status: StatusDone,
		Summary: strings.Repeat("中", 501),
	}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected length error, got %v", err)
	}
}

func TestMeta_StatusOnlyDoneOrFailed(t *testing.T) {
	m := &Meta{TaskID: "t_001", Agent: "x", Status: StatusRunning, Summary: "x"}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Errorf("expected status error, got %v", err)
	}
}

func TestMeta_EmptyTaskID(t *testing.T) {
	m := &Meta{TaskID: "", Agent: "x", Status: StatusDone, Summary: "x"}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "task_id") {
		t.Errorf("expected task_id error, got %v", err)
	}
}

func TestMeta_SummaryExactly500Runes(t *testing.T) {
	m := &Meta{TaskID: "t_001", Agent: "x", Status: StatusDone, Summary: strings.Repeat("中", 500)}
	if err := m.Validate(); err != nil {
		t.Errorf("500-rune summary should be valid, got %v", err)
	}
}
