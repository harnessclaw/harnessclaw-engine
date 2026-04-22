package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
)

// mockArtifactStore implements tool.ArtifactStore for testing.
type mockArtifactStore struct {
	artifacts map[string]tool.ArtifactContent
}

func (m *mockArtifactStore) Get(id string) tool.ArtifactContent {
	return m.artifacts[id]
}

func newMockStore(items map[string]tool.ArtifactContent) *mockArtifactStore {
	return &mockArtifactStore{artifacts: items}
}

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
	if !strings.Contains(desc, "artifact_ref") {
		t.Error("description should mention artifact_ref")
	}
}

func TestInputSchema(t *testing.T) {
	ft := New(enabledCfg())
	schema := ft.InputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	for _, field := range []string{"file_path", "content", "artifact_ref"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema should have %s property", field)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema should have required array")
	}
	if len(required) != 1 || required[0] != "file_path" {
		t.Errorf("required = %v, want [file_path]", required)
	}
}

func TestValidateInput(t *testing.T) {
	ft := New(enabledCfg())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid with content", `{"file_path":"/tmp/test.txt","content":"hello"}`, false},
		{"valid with artifact_ref", `{"file_path":"/tmp/test.txt","artifact_ref":"art_abc12345"}`, false},
		{"valid with both (artifact_ref wins)", `{"file_path":"/tmp/test.txt","content":"hello","artifact_ref":"art_abc12345"}`, false},
		{"missing file_path", `{"content":"hello"}`, true},
		{"empty file_path", `{"file_path":"","content":"hello"}`, true},
		{"relative file_path", `{"file_path":"relative/path.txt","content":"hello"}`, true},
		{"no content or artifact_ref", `{"file_path":"/tmp/test.txt"}`, true},
		{"empty content and no artifact_ref", `{"file_path":"/tmp/test.txt","content":""}`, true},
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

	// Verify file content.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want 'hello world'", string(data))
	}

	// Verify metadata.
	if result.Metadata["file_path"] != path {
		t.Errorf("metadata file_path = %v, want %s", result.Metadata["file_path"], path)
	}
	if result.Metadata["bytes_written"] != 11 {
		t.Errorf("metadata bytes_written = %v, want 11", result.Metadata["bytes_written"])
	}
}

func TestExecuteCreatesParentDirs(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "test.txt")

	input := json.RawMessage(`{"file_path":"` + path + `","content":"nested"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("file content = %q, want 'nested'", string(data))
	}
}

func TestExecutePreservesPermissions(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sh")

	// Create file with executable permission.
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

func TestExecuteArtifactRef(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "from_artifact.txt")

	store := newMockStore(map[string]tool.ArtifactContent{
		"art_abc12345": {ID: "art_abc12345", Content: "artifact content here", Size: 21},
	})
	ctx := tool.WithArtifactStore(context.Background(), store)

	input := json.RawMessage(`{"file_path":"` + path + `","artifact_ref":"art_abc12345"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "artifact content here" {
		t.Errorf("file content = %q, want 'artifact content here'", string(data))
	}
	if result.Metadata["bytes_written"] != 21 {
		t.Errorf("metadata bytes_written = %v, want 21", result.Metadata["bytes_written"])
	}
}

func TestExecuteArtifactRefOverridesContent(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "override.txt")

	store := newMockStore(map[string]tool.ArtifactContent{
		"art_xyz99999": {ID: "art_xyz99999", Content: "from artifact", Size: 13},
	})
	ctx := tool.WithArtifactStore(context.Background(), store)

	// Both content and artifact_ref provided — artifact_ref should win.
	input := json.RawMessage(`{"file_path":"` + path + `","content":"from inline","artifact_ref":"art_xyz99999"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "from artifact" {
		t.Errorf("file content = %q, want 'from artifact'", string(data))
	}
}

func TestExecuteArtifactRefNotFound(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "notfound.txt")

	store := newMockStore(map[string]tool.ArtifactContent{})
	ctx := tool.WithArtifactStore(context.Background(), store)

	input := json.RawMessage(`{"file_path":"` + path + `","artifact_ref":"art_nonexist"}`)
	result, err := ft.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for not-found artifact")
	}
	if !strings.Contains(result.Content, "not found") {
		t.Errorf("content = %q, should mention 'not found'", result.Content)
	}

	// File should not be created.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should not exist when artifact is not found")
	}
}

func TestExecuteArtifactRefNoStore(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "nostore.txt")

	// Context without artifact store.
	input := json.RawMessage(`{"file_path":"` + path + `","artifact_ref":"art_abc12345"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when artifact store not available")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("content = %q, should mention 'not available'", result.Content)
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

func TestExecuteNoContentOrArtifactRef(t *testing.T) {
	ft := New(enabledCfg())
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	// Valid JSON but no content or artifact_ref.
	input := json.RawMessage(`{"file_path":"` + path + `"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when neither content nor artifact_ref provided")
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
			// Only check when we know the tool maps it.
			if tt.lang != "" && lang != tt.lang {
				t.Errorf("language for %s = %v, want %s", tt.ext, lang, tt.lang)
			}
		})
	}
}
