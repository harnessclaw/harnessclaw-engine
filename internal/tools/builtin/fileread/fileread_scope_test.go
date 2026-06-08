package fileread

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

func newTool() *FileReadTool {
	return New(config.ToolConfig{Enabled: true})
}

func TestFileRead_RejectsOutOfScopePath(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "ok")
	deny := filepath.Join(dir, "nope")
	for _, d := range []string{allow, deny} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(deny, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{ReadScope: []string{allow}})
	raw, _ := json.Marshal(map[string]any{"file_path": target})
	res, err := newTool().Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != types.ToolErrorPermissionDenied {
		t.Errorf("expected permission_denied, got %+v", res)
	}
}

func TestFileRead_NoScopeMeansNoRestriction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "any.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": target})
	res, err := newTool().Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != "" {
		t.Errorf("expected success, got error %s", res.ErrorType)
	}
}

func TestFileRead_PrefixedPathInScopeAllowed(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "ok")
	if err := os.MkdirAll(allow, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(allow, "deep", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{ReadScope: []string{allow}})
	raw, _ := json.Marshal(map[string]any{"file_path": target})
	res, err := newTool().Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != "" {
		t.Errorf("expected success, got %+v", res)
	}
}
