package plantool

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
)

func TestPlanUpdate_CreateTaskCreatesDir(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})

	raw, _ := json.Marshal(map[string]any{
		"op": "create_task",
		"task": map[string]any{
			"id": "t_001", "title": "调研", "agent": "researcher",
		},
		"session_id": sid,
	})
	res, err := pt.Execute(ctx, raw)
	if err != nil || res.ErrorType != "" {
		t.Fatalf("execute: err=%v result=%+v", err, res)
	}
	if _, err := os.Stat(workspace.TaskDir(root, sid, "t_001")); err != nil {
		t.Errorf("task dir not created: %v", err)
	}
	b, _ := os.ReadFile(workspace.PlanPath(root, sid))
	var p workspace.Plan
	_ = json.Unmarshal(b, &p)
	if p.Tasks["t_001"] == nil || p.Tasks["t_001"].Title != "调研" {
		t.Errorf("plan not updated: %+v", p)
	}
}

func TestPlanUpdate_DirRollbackOnPlanFailure(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(workspace.PlanPath(root, sid), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	raw, _ := json.Marshal(map[string]any{
		"op": "create_task",
		"task": map[string]any{
			"id": "t_001", "title": "x", "agent": "y",
		},
		"session_id": sid,
	})
	res, _ := pt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Fatalf("expected error on malformed plan.json")
	}
	if _, err := os.Stat(workspace.TaskDir(root, sid, "t_001")); !os.IsNotExist(err) {
		t.Errorf("expected dir rolled back, err=%v", err)
	}
}

func TestPlanUpdate_UpdateStatusRunningSetsStartedAt(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)
	ctx := context.Background()

	createRaw, _ := json.Marshal(map[string]any{
		"op":         "create_task",
		"session_id": sid,
		"task":       map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}
	updRaw, _ := json.Marshal(map[string]any{
		"op": "update_status", "session_id": sid, "task_id": "t_001", "status": "running",
	})
	if r, _ := pt.Execute(ctx, updRaw); r.ErrorType != "" {
		t.Fatalf("update: %+v", r)
	}
	b, _ := os.ReadFile(workspace.PlanPath(root, sid))
	var p workspace.Plan
	_ = json.Unmarshal(b, &p)
	if p.Tasks["t_001"].Status != workspace.StatusRunning {
		t.Errorf("status not running: %v", p.Tasks["t_001"].Status)
	}
	if p.Tasks["t_001"].StartedAt.IsZero() {
		t.Errorf("StartedAt not set")
	}
}

func TestPlanUpdate_UpdateStatusDoneRequiresSummaryRef(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)
	ctx := context.Background()

	createRaw, _ := json.Marshal(map[string]any{
		"op":         "create_task",
		"session_id": sid,
		"task":       map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}
	updRaw, _ := json.Marshal(map[string]any{
		"op": "update_status", "session_id": sid, "task_id": "t_001", "status": "done",
	})
	r, _ := pt.Execute(ctx, updRaw)
	if r.ErrorType == "" {
		t.Errorf("expected error for status=done without summary_ref")
	}
}

func TestPlanUpdate_WipeForRetryIncrementsAttempt(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)
	ctx := context.Background()

	createRaw, _ := json.Marshal(map[string]any{
		"op":         "create_task",
		"session_id": sid,
		"task":       map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}
	wipeRaw, _ := json.Marshal(map[string]any{
		"op": "wipe_for_retry", "session_id": sid, "task_id": "t_001",
	})
	if r, _ := pt.Execute(ctx, wipeRaw); r.ErrorType != "" {
		t.Fatalf("wipe: %+v", r)
	}
	b, _ := os.ReadFile(workspace.PlanPath(root, sid))
	var p workspace.Plan
	_ = json.Unmarshal(b, &p)
	if p.Tasks["t_001"].Attempt != 2 {
		t.Errorf("attempt = %d, want 2", p.Tasks["t_001"].Attempt)
	}
	if p.Tasks["t_001"].Status != workspace.StatusPending {
		t.Errorf("status = %v, want pending", p.Tasks["t_001"].Status)
	}
}
