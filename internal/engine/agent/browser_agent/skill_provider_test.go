package browser_agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

func defaultCfg() config.BrowserAgentConfig {
	return config.BrowserAgentConfig{
		Enabled:       true,
		SkillMaxBytes: 0, // unlimited
	}
}

func writeSkill(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}

func TestAgentBrowserSkillProvider_LoadsEmbeddedSkill(t *testing.T) {
	p := NewAgentBrowserSkillProvider(defaultCfg(), zap.NewNop())

	full, err := p.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if full.Path != "embedded://agent-browser/SKILL.md" {
		t.Fatalf("Path = %q", full.Path)
	}
	if !strings.Contains(full.Body, "# Browser Automation with agent-browser") {
		t.Fatalf("embedded body missing agent-browser skill content")
	}
	if !strings.HasPrefix(full.Body, adapterHeader) {
		t.Fatalf("embedded body missing adapter header")
	}
}

func TestAgentBrowserSkillProvider_LoadsInjectedFileSkill(t *testing.T) {
	cfg := defaultCfg()
	sourcePath := writeSkill(t, "OFFICIAL PACKAGED SKILL BODY")
	p := newAgentBrowserSkillProviderForTest(cfg, sourcePath, zap.NewNop())

	full, err := p.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if full.Name != "agent-browser/core" {
		t.Errorf("Name = %q, want agent-browser/core", full.Name)
	}
	if full.Version != "embedded" {
		t.Errorf("Version = %q, want embedded", full.Version)
	}
	if full.Path != sourcePath {
		t.Errorf("Path = %q, want %q", full.Path, sourcePath)
	}
	if !strings.HasPrefix(full.Body, adapterHeader) {
		t.Errorf("body does not start with adapter header:\n%s", full.Body[:min(200, len(full.Body))])
	}
	if !strings.Contains(full.Body, "OFFICIAL PACKAGED SKILL BODY") {
		t.Errorf("body missing original content:\n%s", full.Body)
	}
}

func TestAgentBrowserSkillProvider_LoadsOnlyMainSkillBody(t *testing.T) {
	cfg := defaultCfg()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(sourcePath, []byte("MAIN SKILL BODY\nreferences/commands.md"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	refDir := filepath.Join(dir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "commands.md"), []byte("REFERENCE BODY MUST BE ON DEMAND"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	p := newAgentBrowserSkillProviderForTest(cfg, sourcePath, zap.NewNop())
	full, err := p.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(full.Body, "MAIN SKILL BODY") {
		t.Fatalf("body missing main skill:\n%s", full.Body)
	}
	if strings.Contains(full.Body, "REFERENCE BODY MUST BE ON DEMAND") {
		t.Fatalf("body should not inline references:\n%s", full.Body)
	}
}

func TestAgentBrowserSkillProvider_MissingInjectedFileSkillFails(t *testing.T) {
	cfg := defaultCfg()
	sourcePath := filepath.Join(t.TempDir(), "missing", "SKILL.md")
	p := newAgentBrowserSkillProviderForTest(cfg, sourcePath, zap.NewNop())

	_, err := p.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for missing injected file skill, got nil")
	}
	if !strings.Contains(err.Error(), sourcePath) {
		t.Fatalf("error should include injected file skill path, got %q", err.Error())
	}
}

func TestAgentBrowserSkillProvider_EmptyBodyFails(t *testing.T) {
	cfg := defaultCfg()
	sourcePath := writeSkill(t, "")
	p := newAgentBrowserSkillProviderForTest(cfg, sourcePath, zap.NewNop())

	_, err := p.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention empty", err.Error())
	}
}

func TestAgentBrowserSkillProvider_OversizeBodyFails(t *testing.T) {
	cfg := defaultCfg()
	cfg.SkillMaxBytes = 10
	sourcePath := writeSkill(t, "this body is definitely more than ten bytes long")
	p := newAgentBrowserSkillProviderForTest(cfg, sourcePath, zap.NewNop())

	_, err := p.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for oversize body, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q should mention too large", err.Error())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
