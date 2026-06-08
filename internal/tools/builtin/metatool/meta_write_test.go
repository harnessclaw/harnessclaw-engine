package metatool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/legacy/workspace"
)

func TestMetaWrite_HappyPath(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	tid := "t_001"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
		t.Fatal(err)
	}
	taskDir := workspace.TaskDir(root, sid, tid)
	outPath := filepath.Join(taskDir, "output.md")
	if err := os.WriteFile(outPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	mt := NewMetaWriteTool(root)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		WriteScope:  []string{taskDir},
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
		Agent:       "researcher",
	})
	raw, _ := json.Marshal(map[string]any{
		"status":  "done",
		"summary": "对比 5 家产品",
		"outputs": []map[string]any{{"path": outPath, "type": "markdown"}},
	})
	res, err := mt.Execute(ctx, raw)
	if err != nil || res.ErrorType != "" {
		t.Fatalf("execute: err=%v result=%+v", err, res)
	}
	b, _ := os.ReadFile(workspace.MetaPath(root, sid, tid))
	var m workspace.Meta
	_ = json.Unmarshal(b, &m)
	if m.Summary != "对比 5 家产品" {
		t.Errorf("meta not written: %+v", m)
	}
	if m.TaskID != tid {
		t.Errorf("TaskID from ctx not applied: %q != %q", m.TaskID, tid)
	}
	if m.Agent != "researcher" {
		t.Errorf("Agent from ctx not applied: %q", m.Agent)
	}
	if len(m.Outputs) != 1 || m.Outputs[0].Bytes != 5 {
		t.Errorf("bytes should be stat-filled from real file size 5; got outputs=%+v", m.Outputs)
	}
}

func TestMetaWrite_RejectsSecondCall(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	tid := "t_001"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
		t.Fatal(err)
	}
	mt := NewMetaWriteTool(root)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
		Agent:       "x",
	})
	raw, _ := json.Marshal(map[string]any{"status": "done", "summary": "x"})
	if res, _ := mt.Execute(ctx, raw); res.ErrorType != "" {
		t.Fatalf("first call: %+v", res)
	}
	res, _ := mt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Errorf("second call should fail (O_EXCL)")
	}
}

func TestMetaWrite_ValidatesSummaryRequired(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	tid := "t_001"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
		t.Fatal(err)
	}
	mt := NewMetaWriteTool(root)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
		Agent:       "x",
	})
	raw, _ := json.Marshal(map[string]any{"status": "done", "summary": ""})
	res, _ := mt.Execute(ctx, raw)
	if res.ErrorType == "" {
		t.Errorf("expected validation error for empty summary, got %+v", res)
	}
}
