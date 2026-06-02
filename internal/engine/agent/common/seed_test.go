package common

import (
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
