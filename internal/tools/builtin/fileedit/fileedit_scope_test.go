package fileedit

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

func newTool() *FileEditTool {
	return New(config.ToolConfig{Enabled: true})
}

func TestFileEdit_RejectsOutOfScopePath(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "ok")
	deny := filepath.Join(dir, "nope")
	for _, d := range []string{allow, deny} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(deny, "secret.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{WriteScope: []string{allow}})
	raw, _ := json.Marshal(map[string]any{
		"file_path":  target,
		"old_string": "hello",
		"new_string": "world",
	})
	res, err := newTool().Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != types.ToolErrorPermissionDenied {
		t.Errorf("expected permission_denied, got %+v", res)
	}
}

func TestFileEdit_NoScopeMeansNoRestriction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"file_path":  target,
		"old_string": "hello",
		"new_string": "world",
	})
	res, err := newTool().Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != "" {
		t.Errorf("expected success, got %+v", res)
	}
}
