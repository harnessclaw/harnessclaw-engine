package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
)

func enabledCfg() config.ToolConfig {
	return config.ToolConfig{Enabled: true}
}

func TestName(t *testing.T) {
	ft := New(enabledCfg())
	if ft.Name() != "write" {
		t.Errorf("Name() = %q, want Write", ft.Name())
	}
}

func TestIsReadOnly(t *testing.T) {
	ft := New(enabledCfg())
	if ft.IsReadOnly() {
		t.Error("Write tool should not be read-only")
	}
}

func TestIsEnabled(t *testing.T) {
	enabled := New(config.ToolConfig{Enabled: true})
	if !enabled.IsEnabled() {
		t.Error("should be enabled when config says so")
	}
	disabled := New(config.ToolConfig{Enabled: false})
	if disabled.IsEnabled() {
		t.Error("should be disabled when config says so")
	}
}

func TestDescription(t *testing.T) {
	ft := New(enabledCfg())
	desc := ft.Description()
	if desc == "" {
		t.Error("description should not be empty")
	}
}

func TestInputSchema(t *testing.T) {
	ft := New(enabledCfg())
	schema := ft.InputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	for _, field := range []string{"file_path", "content"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema should have %s property", field)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema should have required array")
	}
	if len(required) != 2 {
		t.Errorf("required = %v, want [file_path content]", required)
	}
}

func TestValidateInput(t *testing.T) {
	ft := New(enabledCfg())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", `{"file_path":"/tmp/test.txt","content":"hello"}`, false},
		{"missing file_path", `{"content":"hello"}`, true},
		{"empty file_path", `{"file_path":"","content":"hello"}`, true},
		{"relative file_path", `{"file_path":"relative/path.txt","content":"hello"}`, true},
		{"missing content", `{"file_path":"/tmp/test.txt"}`, true},
		{"empty content", `{"file_path":"/tmp/test.txt","content":""}`, true},
		{"invalid json", `not json`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ft.ValidateInput(json.RawMessage(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecuteWriteContent(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	input := json.RawMessage(`{"file_path":"` + path + `","content":"hello world"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Successfully wrote") {
		t.Errorf("content = %q, want success message", result.Content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want 'hello world'", string(data))
	}

	if result.Metadata["file_path"] != path {
		t.Errorf("metadata file_path = %v, want %s", result.Metadata["file_path"], path)
	}
	if result.Metadata["bytes_written"] != 11 {
		t.Errorf("metadata bytes_written = %v, want 11", result.Metadata["bytes_written"])
	}
}

// TestExecute_AutoMkdir_WithinTaskDir 验证白名单内（task_dir 子树下）的
// 写入会自动创建父目录。task_dir = {SessionRoot}/tasks/{TaskID}。
func TestExecute_AutoMkdir_WithinTaskDir(t *testing.T) {
	ft := New(enabledCfg())
	sessionRoot := t.TempDir()
	taskID := "t-abc"
	taskDir := filepath.Join(sessionRoot, "tasks", taskID)
	path := filepath.Join(taskDir, "sub", "deep", "file.md") // 嵌套不存在的目录

	scope := tool.AgentScope{
		SessionRoot: sessionRoot,
		TaskID:      taskID,
	}
	ctx := tool.WithAgentScope(context.Background(), scope)

	input := json.RawMessage(`{"file_path":"` + path + `","content":"hello"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success (auto-mkdir within task_dir), got error: %s", result.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("file content = %q", string(got))
	}
}

// TestExecute_RejectsOutsideTaskDir 验证白名单外的路径维持"父目录必须
// 已存在"约束。这是沙箱保护：LLM 写错路径不应在任意位置 mkdir。
func TestExecute_RejectsOutsideTaskDir(t *testing.T) {
	ft := New(enabledCfg())
	sessionRoot := t.TempDir()
	outsideRoot := t.TempDir() // 完全独立的目录
	taskID := "t-abc"
	path := filepath.Join(outsideRoot, "nonexistent", "file.md") // task_dir 之外

	scope := tool.AgentScope{
		SessionRoot: sessionRoot,
		TaskID:      taskID,
	}
	ctx := tool.WithAgentScope(context.Background(), scope)

	input := json.RawMessage(`{"file_path":"` + path + `","content":"x"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when writing outside task_dir to nonexistent parent")
	}
	if !strings.Contains(result.Content, "does not exist") {
		t.Errorf("error should mention dir not existing, got: %s", result.Content)
	}
	// 验证没在 outsideRoot 下偷偷建目录
	if _, statErr := os.Stat(filepath.Dir(path)); statErr == nil {
		t.Error("parent dir should NOT have been created outside task_dir")
	}
}

// TestExecute_NoScopeFallsBackToStrict 验证没 AgentScope 的 ctx（legacy 路径 /
// 单元测试默认 ctx）走严格分支：父目录必须已存在。
func TestExecute_NoScopeFallsBackToStrict(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "test.txt")

	input := json.RawMessage(`{"file_path":"` + path + `","content":"x"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when no AgentScope in ctx and parent missing")
	}
}

// TestExecute_PrefixBoundaryDoesNotLeak 验证白名单匹配做的是路径段比较，
// 而非字符串前缀比较 —— "/foo/tasks/t1-evil" 不能命中 "/foo/tasks/t1"。
func TestExecute_PrefixBoundaryDoesNotLeak(t *testing.T) {
	ft := New(enabledCfg())
	sessionRoot := t.TempDir()
	taskID := "t1"
	// 构造一个跟 task_dir 同前缀但不同子目录的 evil 路径
	evilPath := filepath.Join(sessionRoot, "tasks", "t1-evil", "deep", "file.md")

	scope := tool.AgentScope{
		SessionRoot: sessionRoot,
		TaskID:      taskID,
	}
	ctx := tool.WithAgentScope(context.Background(), scope)

	input := json.RawMessage(`{"file_path":"` + evilPath + `","content":"x"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error: t1-evil sibling must NOT be treated as within task_dir t1")
	}
}

func TestExecutePreservesPermissions(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sh")

	if err := os.WriteFile(path, []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatalf("failed to create initial file: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + path + `","content":"#!/bin/sh\necho updated"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if info.Mode() != 0755 {
		t.Errorf("file mode = %v, want 0755", info.Mode())
	}
}

func TestExecuteInvalidInput(t *testing.T) {
	ft := New(enabledCfg())

	input := json.RawMessage(`invalid json`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for invalid json")
	}
}

func TestExecuteLanguageDetection(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()

	tests := []struct {
		ext  string
		lang string
	}{
		{".go", "go"},
		{".py", "python"},
		{".js", "javascript"},
		{".ts", "typescript"},
		{".txt", ""},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			path := filepath.Join(dir, "test"+tt.ext)
			input := json.RawMessage(`{"file_path":"` + path + `","content":"content"}`)
			result, err := ft.Execute(context.Background(), input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Errorf("unexpected error: %s", result.Content)
			}
			lang := result.Metadata["language"]
			if tt.lang != "" && lang != tt.lang {
				t.Errorf("language for %s = %v, want %s", tt.ext, lang, tt.lang)
			}
		})
	}
}
