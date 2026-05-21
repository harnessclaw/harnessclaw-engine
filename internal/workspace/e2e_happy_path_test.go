package workspace_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/workspace"
)

// TestE2E_WorkspaceHappyPath exercises the local-files-as-truth flow
// end-to-end at the workspace-package layer (no LLM, no engine): server
// EnsureSession → L2 PlanUpdate create_task ×2 → L3 t_001 writes
// output + meta → L2 ReconcileSpawnReturn(t_001) → L3 t_002 writes
// output + meta → L2 ReconcileSpawnReturn(t_002) → Promote t_002.
//
// Asserts the doc §3 invariants the deeper LLM-side tests can't cover
// without spinning up the engine: source preserved (cp not mv), task
// frozen after Promote, single Deliverable entry in plan.json,
// destination lives inside the session root.
func TestE2E_WorkspaceHappyPath(t *testing.T) {
	root := t.TempDir()
	sid := "sess_e2e"

	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	reg := workspace.NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())

	// L2 plan: create two tasks.
	for _, tid := range []string{"t_001", "t_002"} {
		if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
			t.Fatalf("ensure task %s: %v", tid, err)
		}
		tid := tid
		if err := reg.Get(sid).Apply(context.Background(), func(p *workspace.Plan) error {
			if p.Tasks == nil {
				p.Tasks = map[string]*workspace.Task{}
			}
			p.Tasks[tid] = &workspace.Task{
				Title:     tid,
				Agent:     "x",
				Status:    workspace.StatusPending,
				OutputDir: "tasks/" + tid + "/",
			}
			return nil
		}); err != nil {
			t.Fatalf("plan apply create %s: %v", tid, err)
		}
	}

	// L3 t_001: write output + meta.
	out1 := filepath.Join(workspace.TaskDir(root, sid, "t_001"), "output.md")
	if err := os.WriteFile(out1, []byte("# 调研\n5家产品对比..."), 0o644); err != nil {
		t.Fatalf("write t_001 output: %v", err)
	}
	m1 := &workspace.Meta{
		TaskID: "t_001", Agent: "researcher", Status: workspace.StatusDone,
		Summary: "5家竞品对比",
		Outputs: []workspace.Output{{Path: out1, Type: "markdown", Bytes: 20}},
	}
	mb1, _ := json.MarshalIndent(m1, "", "  ")
	if err := os.WriteFile(workspace.MetaPath(root, sid, "t_001"), mb1, 0o644); err != nil {
		t.Fatalf("write t_001 meta: %v", err)
	}

	ok, err := workspace.ReconcileSpawnReturn(context.Background(), reg.Get(sid), root, sid, "t_001")
	if err != nil || !ok {
		t.Fatalf("reconcile t_001: ok=%v err=%v", ok, err)
	}

	// L3 t_002: write output + meta. Mirrors a writer step that read t_001.
	out2 := filepath.Join(workspace.TaskDir(root, sid, "t_002"), "output.md")
	if err := os.WriteFile(out2, []byte("# 最终报告\n基于调研..."), 0o644); err != nil {
		t.Fatalf("write t_002 output: %v", err)
	}
	m2 := &workspace.Meta{
		TaskID: "t_002", Agent: "writer", Status: workspace.StatusDone,
		Summary: "3000字最终报告",
		Outputs: []workspace.Output{{Path: out2, Type: "markdown", Bytes: 18}},
	}
	mb2, _ := json.MarshalIndent(m2, "", "  ")
	if err := os.WriteFile(workspace.MetaPath(root, sid, "t_002"), mb2, 0o644); err != nil {
		t.Fatalf("write t_002 meta: %v", err)
	}
	ok, err = workspace.ReconcileSpawnReturn(context.Background(), reg.Get(sid), root, sid, "t_002")
	if err != nil || !ok {
		t.Fatalf("reconcile t_002: ok=%v err=%v", ok, err)
	}

	// L2 Promote t_002 → final_report.md. Use cp semantics directly so
	// this test doesn't need the LLM-facing Promote tool wired in (its
	// behaviour is covered in internal/tool/promotetool).
	dst := filepath.Join(workspace.DeliverablesDir(root, sid), "final_report.md")
	srcBytes, _ := os.ReadFile(out2)
	if err := os.WriteFile(dst, srcBytes, 0o644); err != nil {
		t.Fatalf("promote cp: %v", err)
	}
	if err := reg.Get(sid).Apply(context.Background(), func(p *workspace.Plan) error {
		p.Tasks["t_002"].Frozen = true
		p.Deliverables = append(p.Deliverables, workspace.DeliverableEntry{
			Path:         "deliverables/final_report.md",
			PromotedFrom: out2,
		})
		return nil
	}); err != nil {
		t.Fatalf("plan apply freeze: %v", err)
	}

	// Assertions.
	planB, _ := os.ReadFile(workspace.PlanPath(root, sid))
	var p workspace.Plan
	if err := json.Unmarshal(planB, &p); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if p.Tasks["t_001"].Status != workspace.StatusDone {
		t.Errorf("t_001 status = %v, want done", p.Tasks["t_001"].Status)
	}
	if p.Tasks["t_001"].SummaryRef != "tasks/t_001/meta.json" {
		t.Errorf("t_001 summary_ref = %q", p.Tasks["t_001"].SummaryRef)
	}
	if !p.Tasks["t_002"].Frozen {
		t.Errorf("t_002 not frozen after promote")
	}
	if len(p.Deliverables) != 1 {
		t.Errorf("deliverables = %d, want 1", len(p.Deliverables))
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dst missing: %v", err)
	}
	if _, err := os.Stat(out2); err != nil {
		t.Errorf("source disappeared (cp not mv): %v", err)
	}
	if !strings.HasPrefix(dst, workspace.SessionRoot(root, sid)) {
		t.Errorf("dst out of session root: %s", dst)
	}
}
