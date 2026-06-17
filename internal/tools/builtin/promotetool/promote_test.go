package promotetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// withTaskFiles 在 task_dir 里建好若干文件作为测试 fixture。
// 返回 task_dir 绝对路径供后续断言。
func withTaskFiles(t *testing.T, root, sid, tid string, files map[string]string) string {
	t.Helper()
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	taskDir := workspace.TaskDir(root, sid, tid)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir taskDir: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(taskDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return taskDir
}

// ctxWithSession 模拟 emma 主路径在 ctx 注入的 root session id (core.go 行为)。
func ctxWithSession(sid string) context.Context {
	ctx := sessionstats.WithSessionID(context.Background(), sid)
	return sessionstats.WithRootSessionID(ctx, sid)
}

// promotionsJSON 是测试用的入参助手——避免每个测试都手写一遍。
func promotionsJSON(taskID string, items []promotion) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"task_id":    taskID,
		"promotions": items,
	})
	return b
}

func TestPromote_HappyPath_KeepsOriginalName(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_abc", "t-abc12345"
	withTaskFiles(t, root, sid, tid, map[string]string{
		"plan.md":   "# plan body",
		"notes.txt": "scratch notes",
	})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{{Source: "plan.md"}})
	res, err := pt.Execute(ctxWithSession(sid), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	// 默认保持原名 —— deliverables/plan.md, 不带 task_id 前缀
	deliverDir := workspace.DeliverablesDir(root, sid)
	want := filepath.Join(deliverDir, "plan.md")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read promoted file: %v", err)
	}
	if string(body) != "# plan body" {
		t.Errorf("promoted content = %q", string(body))
	}
	// 确认 deliverables/ 下不应该出现旧版本的 t-xxx__ 前缀文件
	if _, err := os.Stat(filepath.Join(deliverDir, "t-abc12345__plan.md")); err == nil {
		t.Errorf("should NOT have t-xxx__ prefix anymore")
	}
	// scratch 文件没被 promote
	if _, err := os.Stat(filepath.Join(deliverDir, "notes.txt")); err == nil {
		t.Errorf("notes.txt should NOT have been promoted")
	}
	// 源文件 (cp 而非 mv) 仍在 task_dir
	src := filepath.Join(workspace.TaskDir(root, sid, tid), "plan.md")
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source plan.md should remain in task_dir: %v", err)
	}

	// metadata 返回了 promoted list
	promoted, ok := res.Metadata["promoted"].([]promotedItem)
	if !ok || len(promoted) != 1 {
		t.Fatalf("metadata.promoted shape wrong: %T %v", res.Metadata["promoted"], res.Metadata["promoted"])
	}
	if promoted[0].Path != want {
		t.Errorf("promoted[0].Path = %q, want %q", promoted[0].Path, want)
	}
}

func TestPromote_ExplicitAs_Renames(t *testing.T) {
	// LLM 显式指定 as 字段把文件改名后 promote
	root := t.TempDir()
	sid, tid := "sess_a", "t-a"
	withTaskFiles(t, root, sid, tid, map[string]string{"report.md": "v1"})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{
		{Source: "report.md", As: "q4_sales_report.md"},
	})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	deliverDir := workspace.DeliverablesDir(root, sid)
	if _, err := os.Stat(filepath.Join(deliverDir, "q4_sales_report.md")); err != nil {
		t.Errorf("expected q4_sales_report.md in deliverables: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deliverDir, "report.md")); err == nil {
		t.Errorf("report.md should NOT exist (was renamed to q4_sales_report.md)")
	}
}

func TestPromote_NameCollision_Rejected(t *testing.T) {
	// deliverables/ 下已经有 report.md（之前 promote 留的），LLM 再次 promote
	// 应该报错+提示加可读性后缀
	root := t.TempDir()
	sid, tid := "sess_x", "t-x"
	withTaskFiles(t, root, sid, tid, map[string]string{"report.md": "new version"})

	// 预先在 deliverables/ 下占位 (模拟之前其他 task promote 留下的同名文件)
	deliverDir := workspace.DeliverablesDir(root, sid)
	_ = os.MkdirAll(deliverDir, 0o755)
	if err := os.WriteFile(filepath.Join(deliverDir, "report.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("seed deliverable: %v", err)
	}

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{{Source: "report.md"}})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if !res.IsError {
		t.Fatal("expected IsError when target name already exists in deliverables/")
	}
	// 错误信息应该提示 LLM 加 "as" 字段 + 给可读性命名建议
	for _, want := range []string{"已存在", "as", "可读性"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("error message should contain %q to guide LLM, got: %s", want, res.Content)
		}
	}
	// 占位文件不应被改写
	body, _ := os.ReadFile(filepath.Join(deliverDir, "report.md"))
	if string(body) != "old" {
		t.Errorf("existing report.md should not be overwritten, got %q", string(body))
	}
}

func TestPromote_NameCollision_ResolvedByAs(t *testing.T) {
	// 模拟 LLM 收到冲突错误后，第二次调 promote 加上 as 重命名
	root := t.TempDir()
	sid, tid := "sess_a", "t-second"
	withTaskFiles(t, root, sid, tid, map[string]string{"report.md": "new version"})

	deliverDir := workspace.DeliverablesDir(root, sid)
	_ = os.MkdirAll(deliverDir, 0o755)
	_ = os.WriteFile(filepath.Join(deliverDir, "report.md"), []byte("old"), 0o644)

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{
		{Source: "report.md", As: "report_v2.md"},
	})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if res.IsError {
		t.Fatalf("unexpected error after rename: %s", res.Content)
	}
	// 两个文件并存
	for _, name := range []string{"report.md", "report_v2.md"} {
		if _, err := os.Stat(filepath.Join(deliverDir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestPromote_MultiplePromotions(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_m", "t-multi"
	withTaskFiles(t, root, sid, tid, map[string]string{
		"a.md": "A", "b.md": "B", "c.md": "C",
	})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{
		{Source: "a.md"},
		{Source: "b.md", As: "renamed_b.md"},
	})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	deliverDir := workspace.DeliverablesDir(root, sid)
	for _, name := range []string{"a.md", "renamed_b.md"} {
		if _, err := os.Stat(filepath.Join(deliverDir, name)); err != nil {
			t.Errorf("expected %s in deliverables: %v", name, err)
		}
	}
	// c.md 没被 promote
	if _, err := os.Stat(filepath.Join(deliverDir, "c.md")); err == nil {
		t.Errorf("c.md should NOT be promoted (not in promotions list)")
	}
}

func TestPromote_TaskDirMissing(t *testing.T) {
	root := t.TempDir()
	sid := "sess_x"
	_ = workspace.EnsureSession(root, sid)

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON("t-missing", []promotion{{Source: "foo.md"}})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if !res.IsError {
		t.Error("expected error when task_dir doesn't exist")
	}
	if !strings.Contains(res.Content, "task_dir") {
		t.Errorf("error should mention missing task_dir: %q", res.Content)
	}
}

func TestPromote_SourceFileMissing(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t-a"
	withTaskFiles(t, root, sid, tid, map[string]string{"exists.md": "hi"})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{{Source: "nonexistent.md"}})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if !res.IsError {
		t.Error("expected error when source file doesn't exist in task_dir")
	}
}

func TestPromote_DuplicateSourceInSameCall(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_x", "t-x"
	withTaskFiles(t, root, sid, tid, map[string]string{"r.md": "v"})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{
		{Source: "r.md"},
		{Source: "r.md", As: "r2.md"}, // 同一 source 两次
	})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if !res.IsError {
		t.Error("expected error on duplicate source in single call")
	}
}

func TestPromote_DuplicateTargetInSameCall(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_x", "t-x"
	withTaskFiles(t, root, sid, tid, map[string]string{"a.md": "A", "b.md": "B"})

	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON(tid, []promotion{
		{Source: "a.md", As: "x.md"},
		{Source: "b.md", As: "x.md"}, // 两个 source 想同名进 deliverables/
	})
	res, _ := pt.Execute(ctxWithSession(sid), raw)
	if !res.IsError {
		t.Error("expected error when two promotions target the same deliverable name")
	}
}

func TestPromote_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_p", "t-p"
	withTaskFiles(t, root, sid, tid, map[string]string{"safe.md": "ok"})

	pt := NewPromoteTool(root, nil)
	// source 端攻击
	for _, badSource := range []string{"../etc/passwd", "./", "..", "sub/file.md", "/abs/path"} {
		raw := promotionsJSON(tid, []promotion{{Source: badSource}})
		res, _ := pt.Execute(ctxWithSession(sid), raw)
		if !res.IsError {
			t.Errorf("source %q should be rejected", badSource)
		}
	}
	// as 端攻击（即使 source 合法，as 不能逃逸）
	for _, badAs := range []string{"../etc/passwd", "sub/file.md", "/abs/path"} {
		raw := promotionsJSON(tid, []promotion{{Source: "safe.md", As: badAs}})
		res, _ := pt.Execute(ctxWithSession(sid), raw)
		if !res.IsError {
			t.Errorf("as %q should be rejected", badAs)
		}
	}
}

func TestPromote_NoSessionInCtx(t *testing.T) {
	root := t.TempDir()
	pt := NewPromoteTool(root, nil)
	raw := promotionsJSON("t-x", []promotion{{Source: "f.md"}})
	res, _ := pt.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("expected error when ctx lacks RootSessionID")
	}
	if !strings.Contains(res.Content, "session_id") {
		t.Errorf("error should mention session_id: %s", res.Content)
	}
}

func TestPromote_EmitsDeliverableEvent(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_ev", "t-ev"
	withTaskFiles(t, root, sid, tid, map[string]string{"out.md": "hello"})

	events := make(chan types.EngineEvent, 4)
	pt := NewPromoteTool(root, events)

	raw := promotionsJSON(tid, []promotion{{Source: "out.md"}})
	if res, _ := pt.Execute(ctxWithSession(sid), raw); res.IsError {
		t.Fatalf("promote failed: %s", res.Content)
	}

	select {
	case evt := <-events:
		if evt.Type != types.EngineEventDeliverable {
			t.Errorf("event type = %v, want Deliverable", evt.Type)
		}
		if evt.Deliverable == nil || evt.Deliverable.FilePath == "" {
			t.Errorf("Deliverable payload missing or empty")
		}
		// 验证事件携带的路径是 deliverables/out.md，不是 task_dir 内的
		if !strings.Contains(evt.Deliverable.FilePath, "/deliverables/out.md") {
			t.Errorf("FilePath = %q, want deliverables/out.md", evt.Deliverable.FilePath)
		}
	default:
		t.Error("no Deliverable event emitted")
	}
}
