package searchskill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/skills"
)

func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchSkill_ReturnsMetadataOnly(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "notion-export", "export to notion")
	reader := skill.NewReader([]string{tmp}, zap.NewNop())
	tool := New(reader, zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"query": "notion"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "notion-export") {
		t.Errorf("Content missing skill name: %s", res.Content)
	}
	// Path must NOT appear in tool output (would leak absolute path)
	if strings.Contains(res.Content, tmp) {
		t.Errorf("Content leaks Path: %s", res.Content)
	}
}

func TestSearchSkill_EmptyResults(t *testing.T) {
	tmp := t.TempDir()
	reader := skill.NewReader([]string{tmp}, zap.NewNop())
	tool := New(reader, zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"query": "xyz"})
	res, _ := tool.Execute(context.Background(), raw)
	if res.IsError {
		t.Fatalf("empty result should not be IsError: %s", res.Content)
	}
}
