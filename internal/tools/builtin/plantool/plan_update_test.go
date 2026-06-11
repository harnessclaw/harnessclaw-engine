package plantool

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
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

func TestPlanUpdate_EmitPlanCreatedOnFirstTask(t *testing.T) {
	root := t.TempDir()
	sid := "sess_emit"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	events := make(chan types.EngineEvent, 4)
	ctx := tool.WithEventOut(context.Background(), events)

	raw, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_001", "title": "调研", "agent": "researcher"},
	})
	if r, _ := pt.Execute(ctx, raw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventPlanCreated {
			t.Errorf("want plan_created, got %q", ev.Type)
		}
		if ev.PlanEvent == nil || ev.PlanEvent.PlanID != sid {
			t.Errorf("PlanEvent missing or wrong PlanID: %+v", ev.PlanEvent)
		}
		if ev.PlanEvent.Status != "created" {
			t.Errorf("want status=created, got %q", ev.PlanEvent.Status)
		}
		if len(ev.PlanEvent.Tasks) != 1 || ev.PlanEvent.Tasks[0].TaskID != "t_001" {
			t.Errorf("unexpected Tasks: %+v", ev.PlanEvent.Tasks)
		}
	default:
		t.Error("no plan event emitted")
	}

	// second task → plan_updated
	raw2, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_002", "title": "实现", "agent": "developer"},
	})
	if r, _ := pt.Execute(ctx, raw2); r.ErrorType != "" {
		t.Fatalf("create2: %+v", r)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventPlanUpdated {
			t.Errorf("want plan_updated, got %q", ev.Type)
		}
	default:
		t.Error("no plan event emitted for second task")
	}
}

func TestPlanUpdate_EmitPlanCompletedWhenAllDone(t *testing.T) {
	root := t.TempDir()
	sid := "sess_complete"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	ctx := context.Background()
	createRaw, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}

	// now attach event channel and update to done
	events := make(chan types.EngineEvent, 4)
	ctx2 := tool.WithEventOut(context.Background(), events)
	doneRaw, _ := json.Marshal(map[string]any{
		"op": "update_status", "session_id": sid,
		"task_id": "t_001", "status": "done", "summary_ref": "tasks/t_001/meta.json",
	})
	if r, _ := pt.Execute(ctx2, doneRaw); r.ErrorType != "" {
		t.Fatalf("update: %+v", r)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventPlanCompleted {
			t.Errorf("want plan_completed, got %q", ev.Type)
		}
		if ev.PlanEvent == nil || ev.PlanEvent.Status != "completed" {
			t.Errorf("PlanEvent: %+v", ev.PlanEvent)
		}
	default:
		t.Error("no event emitted")
	}
}

func TestPlanUpdate_EmitPlanFailedWhenAnyFailed(t *testing.T) {
	root := t.TempDir()
	sid := "sess_fail"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	ctx := context.Background()
	createRaw, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}

	events := make(chan types.EngineEvent, 4)
	ctx2 := tool.WithEventOut(context.Background(), events)
	failRaw, _ := json.Marshal(map[string]any{
		"op": "update_status", "session_id": sid,
		"task_id": "t_001", "status": "failed",
	})
	if r, _ := pt.Execute(ctx2, failRaw); r.ErrorType != "" {
		t.Fatalf("update: %+v", r)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventPlanFailed {
			t.Errorf("want plan_failed, got %q", ev.Type)
		}
	default:
		t.Error("no event emitted")
	}
}

func TestPlanUpdate_EmitPlanUpdatedOnWipe(t *testing.T) {
	root := t.TempDir()
	sid := "sess_wipe"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	ctx := context.Background()
	createRaw, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	if r, _ := pt.Execute(ctx, createRaw); r.ErrorType != "" {
		t.Fatalf("create: %+v", r)
	}

	events := make(chan types.EngineEvent, 4)
	ctx2 := tool.WithEventOut(context.Background(), events)
	wipeRaw, _ := json.Marshal(map[string]any{
		"op": "wipe_for_retry", "session_id": sid, "task_id": "t_001",
	})
	if r, _ := pt.Execute(ctx2, wipeRaw); r.ErrorType != "" {
		t.Fatalf("wipe: %+v", r)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventPlanUpdated {
			t.Errorf("want plan_updated, got %q", ev.Type)
		}
	default:
		t.Error("no event emitted")
	}
}

func TestPlanUpdate_NoEmitWithoutChannel(t *testing.T) {
	root := t.TempDir()
	sid := "sess_noemit"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPlanUpdateTool(reg, root)

	// context has no event channel — should succeed without panic
	raw, _ := json.Marshal(map[string]any{
		"op": "create_task", "session_id": sid,
		"task": map[string]any{"id": "t_001", "title": "x", "agent": "y"},
	})
	r, err := pt.Execute(context.Background(), raw)
	if err != nil || r.ErrorType != "" {
		t.Fatalf("execute: err=%v res=%+v", err, r)
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
