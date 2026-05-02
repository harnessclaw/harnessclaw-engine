package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
)

func enabledCfg() config.ToolConfig {
	return config.ToolConfig{Enabled: true}
}

func TestName(t *testing.T) {
	ft := New(enabledCfg())
	if ft.Name() != "Write" {
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

func TestExecuteRequiresExistingDir(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "test.txt")

	input := json.RawMessage(`{"file_path":"` + path + `","content":"nested"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when directory does not exist")
	}
	if !strings.Contains(result.Content, "does not exist") {
		t.Errorf("error message should mention directory does not exist, got: %s", result.Content)
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
