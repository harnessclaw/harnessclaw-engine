package common

import (
	"os"
	"strings"
	"testing"

)

func TestSeedPrompt_NoRootDir_ReturnsPromptVerbatim(t *testing.T) {
	cfg := &SpawnConfig{Prompt: "do work"}
	got := SeedPrompt(cfg, "")
	if got != "do work" {
		t.Errorf("got %q, want verbatim prompt", got)
	}
}

func TestSeedPrompt_NoSession_ReturnsPromptVerbatim(t *testing.T) {
	cfg := &SpawnConfig{Prompt: "do work"}
	got := SeedPrompt(cfg, "/tmp/ws")
	if got != "do work" {
		t.Errorf("got %q, want verbatim prompt (no session id)", got)
	}
}

func TestSeedPrompt_WithTaskID_InjectsTaskDir(t *testing.T) {
	cfg := &SpawnConfig{
		Prompt:        "write a report",
		RootSessionID: "sess_xyz",
		TaskID:        "t-7",
	}
	got := SeedPrompt(cfg, "/tmp/ws")

	if !strings.Contains(got, "/tmp/ws/session/sess_xyz/tasks/t-7") {
		t.Errorf("expected task_dir absolute path in prelude, got: %s", got)
	}
	if !strings.Contains(got, "t-7") {
		t.Errorf("expected task_id in prelude, got: %s", got)
	}
	if !strings.HasSuffix(got, "write a report") {
		t.Errorf("expected user prompt at end, got tail: %s", got[len(got)-50:])
	}
}

func TestSeedPrompt_NoTaskID_FallsBackToSessionRoot(t *testing.T) {
	cfg := &SpawnConfig{
		Prompt:        "explore",
		RootSessionID: "sess_xyz",
	}
	got := SeedPrompt(cfg, "/tmp/ws")
	if !strings.Contains(got, "/tmp/ws/session/sess_xyz") {
		t.Errorf("expected session root in prelude, got: %s", got)
	}
	if strings.Contains(got, "task_dir") {
		t.Errorf("should NOT mention task_dir when TaskID is empty, got: %s", got)
	}
}

// 注：旧 TestEnsureTaskDir_CreatesPerTaskDir 和
// TestEnsureTaskDir_NoOpOnMissingFields 已删 —— EnsureTaskDir 函数本身
// 被废弃（task_dir 改为 lazy 由 write/edit/meta_write 工具内部创建）。
//
// ScanResidualFiles 相关测试仍保留，把 fixture mkdir 改成直接 os.MkdirAll。

func TestScanResidualFiles_ListsFilesNonRecursive(t *testing.T) {
	rootDir := t.TempDir()
	cfg := &SpawnConfig{RootSessionID: "s1", TaskID: "t1"}
	taskDir := rootDir + "/session/s1/tasks/t1"
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir taskDir: %v", err)
	}
	if err := os.WriteFile(taskDir+"/gen.js", []byte("console.log('hi')"), 0o644); err != nil {
		t.Fatalf("write gen.js: %v", err)
	}
	if err := os.WriteFile(taskDir+"/notes.md", []byte("# notes"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	// Nested dir + file inside — must NOT appear (non-recursive on purpose).
	if err := os.MkdirAll(taskDir+"/scratch", 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	if err := os.WriteFile(taskDir+"/scratch/inner.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("write inner: %v", err)
	}

	got := ScanResidualFiles(cfg, rootDir)
	if len(got) != 2 {
		t.Fatalf("expected 2 top-level files, got %d: %+v", len(got), got)
	}
	names := map[string]int64{}
	for _, f := range got {
		names[f.Path] = f.SizeBytes
	}
	if names[taskDir+"/gen.js"] != int64(len("console.log('hi')")) {
		t.Errorf("gen.js size wrong: %+v", names)
	}
	if names[taskDir+"/notes.md"] != int64(len("# notes")) {
		t.Errorf("notes.md size wrong: %+v", names)
	}
	for path := range names {
		if strings.Contains(path, "scratch") {
			t.Errorf("nested file leaked into result: %s", path)
		}
	}
}

func TestScanResidualFiles_NilOnEmptyOrMissing(t *testing.T) {
	rootDir := t.TempDir()
	// Missing fields → nil.
	if got := ScanResidualFiles(nil, rootDir); got != nil {
		t.Errorf("nil cfg should yield nil, got %+v", got)
	}
	if got := ScanResidualFiles(&SpawnConfig{}, rootDir); got != nil {
		t.Errorf("empty cfg should yield nil, got %+v", got)
	}
	// Existing dir but empty → nil (not empty slice — keeps the failure
	// summary from rendering an empty section).
	cfg := &SpawnConfig{RootSessionID: "s2", TaskID: "t2"}
	_ = os.MkdirAll(rootDir+"/session/s2/tasks/t2", 0o755)
	if got := ScanResidualFiles(cfg, rootDir); got != nil {
		t.Errorf("empty dir should yield nil, got %+v", got)
	}
	// Dir that was never created → nil (best-effort, no error).
	if got := ScanResidualFiles(&SpawnConfig{RootSessionID: "nope", TaskID: "nope"}, rootDir); got != nil {
		t.Errorf("missing dir should yield nil, got %+v", got)
	}
}

