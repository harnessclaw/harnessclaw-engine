package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

func TestFileWrite_RejectsOutOfScopePath(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "ok")
	deny := filepath.Join(dir, "nope")
	for _, d := range []string{allow, deny} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{WriteScope: []string{allow}})
	raw, _ := json.Marshal(map[string]any{
		"file_path": filepath.Join(deny, "out.md"),
		"content":   "x",
	})
	res, err := New(enabledCfg()).Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != types.ToolErrorPermissionDenied {
		t.Errorf("expected permission_denied, got %+v", res)
	}
}

func TestFileWrite_NoScopeMeansNoRestriction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.md")
	raw, _ := json.Marshal(map[string]any{
		"file_path": target,
		"content":   "hi",
	})
	res, err := New(enabledCfg()).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.ErrorType != "" {
		t.Errorf("expected success, got %+v", res)
	}
}

func TestFileWrite_DoesNotEmitFileInfoRenderHint(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"file_path": filepath.Join(dir, "out.md"),
		"content":   "hi",
	})
	res, err := New(enabledCfg()).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if got, _ := res.Metadata["render_hint"].(string); got == "file_info" {
		t.Errorf("FileWrite must not emit render_hint=file_info (Promote is the sole Deliverable source)")
	}
}
