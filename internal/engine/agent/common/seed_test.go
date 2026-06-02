package common

import (
	"os"
	"strings"
	"testing"

	"harnessclaw-go/internal/agent"
)

func TestSeedPrompt_NoRootDir_ReturnsPromptVerbatim(t *testing.T) {
	cfg := &agent.SpawnConfig{Prompt: "do work"}
	got := SeedPrompt(cfg, "")
	if got != "do work" {
		t.Errorf("got %q, want verbatim prompt", got)
	}
}

func TestSeedPrompt_NoSession_ReturnsPromptVerbatim(t *testing.T) {
	cfg := &agent.SpawnConfig{Prompt: "do work"}
	got := SeedPrompt(cfg, "/tmp/ws")
	if got != "do work" {
		t.Errorf("got %q, want verbatim prompt (no session id)", got)
	}
}

func TestSeedPrompt_WithTaskID_InjectsTaskDir(t *testing.T) {
	cfg := &agent.SpawnConfig{
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
	cfg := &agent.SpawnConfig{
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

// TestEnsureTaskDir_CreatesPerTaskDir verifies the per-task workspace
// dir gets created so the LLM's first write/edit doesn't fail with
// `directory does not exist`. This was the root cause of the 11:51
// "AI 散文" task hanging: write failed → LLM shelled out mkdir → next
// LLM turn deadlocked. After this fix the dir is in place before the
// LLM ever sees the SeedPrompt that advertises it.
func TestEnsureTaskDir_CreatesPerTaskDir(t *testing.T) {
	rootDir := t.TempDir()
	cfg := &agent.SpawnConfig{
		RootSessionID: "sess_xyz",
		TaskID:        "t-42",
	}
	if err := EnsureTaskDir(cfg, rootDir); err != nil {
		t.Fatalf("EnsureTaskDir: %v", err)
	}
	expected := rootDir + "/session/sess_xyz/tasks/t-42"
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("expected %s to exist, got %v", expected, err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got %v", info.Mode())
	}
}

func TestEnsureTaskDir_NoOpOnMissingFields(t *testing.T) {
	rootDir := t.TempDir()
	tests := []struct {
		name string
		cfg  *agent.SpawnConfig
	}{
		{"nil cfg", nil},
		{"empty rootSession", &agent.SpawnConfig{TaskID: "t-1"}},
		{"empty taskID", &agent.SpawnConfig{RootSessionID: "s"}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if err := EnsureTaskDir(c.cfg, rootDir); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			// Nothing should have been created under rootDir.
			entries, _ := os.ReadDir(rootDir)
			if len(entries) != 0 {
				t.Errorf("expected empty rootDir, got %d entries", len(entries))
			}
		})
	}
}
