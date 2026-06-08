package plantool

import (
	"context"
	"encoding/json"
	"testing"

	"harnessclaw-go/internal/legacy/workspace"
)

// setupPlan creates a session and uses PlanWriterRegistry.Apply to write tasks
// directly to plan.json, returning the registry (caller must StopAll).
func setupPlan(t *testing.T, root, sid string, tasks map[string]*workspace.Task) *workspace.PlanWriterRegistry {
	t.Helper()
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	w := reg.Get(sid)
	err := w.Apply(context.Background(), func(p *workspace.Plan) error {
		if p.Tasks == nil {
			p.Tasks = map[string]*workspace.Task{}
		}
		for id, task := range tasks {
			p.Tasks[id] = task
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestPlanRead_ReturnsReadyList(t *testing.T) {
	root := t.TempDir()
	sid := "sess_ready"
	reg := setupPlan(t, root, sid, map[string]*workspace.Task{
		"step-1": {Title: "step one", Agent: "worker", Status: workspace.StatusPending},
		"step-2": {Title: "step two", Agent: "worker", Status: workspace.StatusPending, DependsOn: []string{"step-1"}},
	})
	defer reg.StopAll(context.Background())

	tool := NewPlanReadTool(root)
	raw, _ := json.Marshal(map[string]any{"session_id": sid})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	var out planReadOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if out.AllDone {
		t.Error("want all_done=false, got true")
	}
	if len(out.Ready) != 1 || out.Ready[0] != "step-1" {
		t.Errorf("want ready=[step-1], got %v", out.Ready)
	}
	if len(out.Tasks) != 2 {
		t.Errorf("want 2 tasks, got %d", len(out.Tasks))
	}
}

func TestPlanRead_AllDoneWhenAllTerminal(t *testing.T) {
	root := t.TempDir()
	sid := "sess_alldone"
	reg := setupPlan(t, root, sid, map[string]*workspace.Task{
		"step-1": {Title: "step one", Agent: "worker", Status: workspace.StatusDone, SummaryRef: "tasks/step-1/meta.json"},
	})
	defer reg.StopAll(context.Background())

	tool := NewPlanReadTool(root)
	raw, _ := json.Marshal(map[string]any{"session_id": sid})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	var out planReadOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if !out.AllDone {
		t.Error("want all_done=true, got false")
	}
	if len(out.Ready) != 0 {
		t.Errorf("want ready=[], got %v", out.Ready)
	}
	if out.HasFailed {
		t.Error("want has_failed=false, got true")
	}
}

func TestPlanRead_HasFailedWhenAnyFailed(t *testing.T) {
	root := t.TempDir()
	sid := "sess_failed"
	reg := setupPlan(t, root, sid, map[string]*workspace.Task{
		"step-1": {Title: "step one", Agent: "worker", Status: workspace.StatusFailed},
	})
	defer reg.StopAll(context.Background())

	tool := NewPlanReadTool(root)
	raw, _ := json.Marshal(map[string]any{"session_id": sid})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	var out planReadOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if !out.HasFailed {
		t.Error("want has_failed=true, got false")
	}
	if !out.AllDone {
		t.Error("want all_done=true (failed is terminal), got false")
	}
}

func TestPlanRead_ErrorOnMissingSession(t *testing.T) {
	root := t.TempDir()

	tool := NewPlanReadTool(root)
	raw, _ := json.Marshal(map[string]any{"session_id": "nonexistent-session"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true, got false; content: %s", res.Content)
	}
}

func TestPlanRead_ReadyAfterDepDone(t *testing.T) {
	root := t.TempDir()
	sid := "sess_depdone"
	reg := setupPlan(t, root, sid, map[string]*workspace.Task{
		"step-1": {Title: "step one", Agent: "worker", Status: workspace.StatusDone, SummaryRef: "tasks/step-1/meta.json"},
		"step-2": {Title: "step two", Agent: "worker", Status: workspace.StatusPending, DependsOn: []string{"step-1"}},
	})
	defer reg.StopAll(context.Background())

	tool := NewPlanReadTool(root)
	raw, _ := json.Marshal(map[string]any{"session_id": sid})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	var out planReadOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if len(out.Ready) != 1 || out.Ready[0] != "step-2" {
		t.Errorf("want ready=[step-2], got %v", out.Ready)
	}
	if out.AllDone {
		t.Error("want all_done=false, got true")
	}
}
