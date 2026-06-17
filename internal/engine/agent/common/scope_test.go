package common

import (
	"testing"

)

func TestBuildAgentScope_FullCfg(t *testing.T) {
	cfg := &SpawnConfig{
		RootSessionID: "sess-1",
		TaskID:        "t-3",
		SubagentType:  "freelancer",
	}
	scope := BuildAgentScope(cfg, "/tmp/root", "fallback")
	if scope.SessionRoot != "/tmp/root/session/sess-1" {
		t.Errorf("SessionRoot = %q, want /tmp/root/session/sess-1", scope.SessionRoot)
	}
	if scope.TaskID != "t-3" {
		t.Errorf("TaskID = %q, want t-3", scope.TaskID)
	}
	if scope.Agent != "freelancer" {
		t.Errorf("Agent = %q, want freelancer (cfg.SubagentType wins over fallback)", scope.Agent)
	}
}

func TestBuildAgentScope_FallbackAgent(t *testing.T) {
	cfg := &SpawnConfig{
		RootSessionID: "sess-1",
		SubagentType:  "", // empty → use fallback
	}
	scope := BuildAgentScope(cfg, "/tmp/root", "fallback_agent")
	if scope.Agent != "fallback_agent" {
		t.Errorf("Agent = %q, want fallback_agent", scope.Agent)
	}
}

// Regression: meta_write / submit_task_result need a non-empty SessionRoot in
// ctx. Empty rootDir or empty RootSessionID must produce SessionRoot=="" so
// the toolexec layer's "any field set ⇒ attach scope" gate still attaches
// the TaskID/Agent fields, and the tools error out clearly instead of
// silently writing somewhere unexpected.
func TestBuildAgentScope_EmptyRootDirOrSessionID(t *testing.T) {
	tests := []struct {
		name    string
		rootDir string
		sessID  string
	}{
		{"empty rootDir", "", "sess-1"},
		{"empty sessionID", "/tmp/root", ""},
		{"both empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &SpawnConfig{RootSessionID: tc.sessID, SubagentType: "x"}
			scope := BuildAgentScope(cfg, tc.rootDir, "x")
			if scope.SessionRoot != "" {
				t.Errorf("SessionRoot = %q, want empty", scope.SessionRoot)
			}
		})
	}
}
