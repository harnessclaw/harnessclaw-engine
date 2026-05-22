package metatool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
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
	})
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid,
		"task_id":    tid,
		"agent":      "researcher",
		"status":     "done",
		"summary":    "对比 5 家产品",
		"outputs":    []map[string]any{{"path": outPath, "type": "markdown", "bytes": 5}},
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
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid, "agent": "x", "status": "done", "summary": "x",
	})
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
	raw, _ := json.Marshal(map[string]any{
		"session_id": sid, "task_id": tid, "agent": "x", "status": "done", "summary": "",
	})
	res, _ := mt.Execute(context.Background(), raw)
	if res.ErrorType == "" {
		t.Errorf("expected validation error for empty summary, got %+v", res)
	}
}
