package promotetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

func setupTask(t *testing.T, root, sid, tid string, outputs []workspace.Output) {
	t.Helper()
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
		t.Fatal(err)
	}
	for _, o := range outputs {
		if err := os.WriteFile(o.Path, []byte("content-"+o.Path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := &workspace.Meta{TaskID: tid, Agent: "x", Status: workspace.StatusDone, Summary: "s", Outputs: outputs}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(workspace.MetaPath(root, sid, tid), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedPlanTask(t *testing.T, root, sid, tid string, task *workspace.Task) {
	t.Helper()
	planP := workspace.PlanPath(root, sid)
	planB, _ := os.ReadFile(planP)
	var p workspace.Plan
	_ = json.Unmarshal(planB, &p)
	if p.Tasks == nil {
		p.Tasks = map[string]*workspace.Task{}
	}
	p.Tasks[tid] = task
	newPlan, _ := json.MarshalIndent(&p, "", "  ")
	if err := os.WriteFile(planP, newPlan, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPromote_HappyPath(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	outPath := filepath.Join(workspace.TaskDir(root, sid, tid), "output.md")
	setupTask(t, root, sid, tid, []workspace.Output{{Path: outPath, Type: "markdown", Bytes: 10}})
	seedPlanTask(t, root, sid, tid, &workspace.Task{
		Title: "x", Agent: "x", Status: workspace.StatusDone,
		SummaryRef: "tasks/" + tid + "/meta.json",
		OutputDir:  "tasks/" + tid + "/",
	})

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())

	events := make(chan types.EngineEvent, 4)
	pt := NewPromoteTool(reg, root, events)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid,
		"mappings": []map[string]any{{"output_index": 0, "target_name": "final.md"}},
	})
	res, err := pt.Execute(ctx, raw)
	if err != nil || res.ErrorType != "" {
		t.Fatalf("execute: err=%v res=%+v", err, res)
	}
	dest := filepath.Join(workspace.DeliverablesDir(root, sid), "final.md")
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest missing: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("source disappeared (cp, not mv): %v", err)
	}
	planB, _ := os.ReadFile(workspace.PlanPath(root, sid))
	var p workspace.Plan
	_ = json.Unmarshal(planB, &p)
	if !p.Tasks[tid].Frozen {
		t.Errorf("plan task not frozen")
	}
	select {
	case ev := <-events:
		if ev.Deliverable == nil || ev.Deliverable.FilePath != dest {
			t.Errorf("event mismatch: %+v", ev)
		}
	default:
		t.Errorf("no Deliverable event emitted")
	}
}

func TestPromote_RejectsAfterFrozen(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	outPath := filepath.Join(workspace.TaskDir(root, sid, tid), "output.md")
	setupTask(t, root, sid, tid, []workspace.Output{{Path: outPath}})
	seedPlanTask(t, root, sid, tid, &workspace.Task{
		Title: "x", Agent: "x", Status: workspace.StatusDone,
		SummaryRef: "x", Frozen: true,
		OutputDir: "tasks/" + tid + "/",
	})

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPromoteTool(reg, root, nil)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid,
		"mappings": []map[string]any{{"output_index": 0, "target_name": "x.md"}},
	})
	res, _ := pt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Errorf("expected frozen rejection")
	}
}

func TestPromote_RollbackOnPlanWriteFailure(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	outPath := filepath.Join(workspace.TaskDir(root, sid, tid), "output.md")
	setupTask(t, root, sid, tid, []workspace.Output{{Path: outPath, Type: "markdown"}})
	seedPlanTask(t, root, sid, tid, &workspace.Task{
		Title: "x", Agent: "x", Status: workspace.StatusDone, SummaryRef: "x",
		OutputDir: "tasks/" + tid + "/",
	})

	// Corrupt plan.json AFTER setup so PlanWriter fails on read inside Apply
	if err := os.WriteFile(workspace.PlanPath(root, sid), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPromoteTool(reg, root, nil)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid,
		"mappings": []map[string]any{{"output_index": 0, "target_name": "rollback.md"}},
	})
	res, _ := pt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Errorf("expected plan write failure")
	}
	dest := filepath.Join(workspace.DeliverablesDir(root, sid), "rollback.md")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("expected dest rolled back, err=%v", err)
	}
}

func TestPromote_DuplicateTargetNameRejected(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	outA := filepath.Join(workspace.TaskDir(root, sid, tid), "a.md")
	outB := filepath.Join(workspace.TaskDir(root, sid, tid), "b.md")
	setupTask(t, root, sid, tid, []workspace.Output{{Path: outA}, {Path: outB}})
	seedPlanTask(t, root, sid, tid, &workspace.Task{
		Title: "x", Agent: "x", Status: workspace.StatusDone, SummaryRef: "x",
		OutputDir: "tasks/" + tid + "/",
	})

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())
	pt := NewPromoteTool(reg, root, nil)
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid,
		"mappings": []map[string]any{
			{"output_index": 0, "target_name": "dup.md"},
			{"output_index": 1, "target_name": "dup.md"},
		},
	})
	res, _ := pt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Errorf("expected duplicate target_name rejection")
	}
}
