package agent

import (
	"context"
	"testing"
	"time"
)

// TestSQLiteAgentStore_RejectsBuiltinCreate is the wire-level guard:
// even if a caller asks the store to persist a builtin, the store
// refuses. Builtins live in code (RegisterBuiltins) — persisting them
// would lock in stale tool palettes across upgrades.
func TestSQLiteAgentStore_RejectsBuiltinCreate(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteAgentStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	tests := []struct {
		name string
		def  *AgentDefinition
	}{
		{
			"is_builtin flag set",
			&AgentDefinition{Name: "x1", AgentType: "sync", Profile: "worker", IsBuiltin: true},
		},
		{
			"source = builtin",
			&AgentDefinition{Name: "x2", AgentType: "sync", Profile: "worker", Source: "builtin"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Create(context.Background(), tc.def)
			if err == nil {
				t.Errorf("expected Create to reject builtin, got nil")
			}
		})
	}
}

// TestSQLiteAgentStore_PurgeBuiltins removes legacy persisted builtins
// (rows where is_builtin=1 OR source='builtin') and leaves user-created
// rows alone.
//
// Real-world repro: a database created by a pre-tier-system binary
// contains a "scheduler" row with display_name="小时", agent_type=sync,
// is_builtin=1 — and on startup LoadAllToRegistry happily overwrites
// the in-code L2 scheduler builtin with that stale shape, stripping
// freelance from the tool palette and breaking L1→L2→L3 dispatch.
// PurgeBuiltins at startup drops these rows; RegisterBuiltins then
// wins on the next LoadAllToRegistry.
func TestSQLiteAgentStore_PurgeBuiltins(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteAgentStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// Directly insert a "legacy builtin" row, bypassing Create's guard
	// (which now refuses such records) — this simulates a row created
	// by an older binary.
	_, err = store.db.Exec(`INSERT INTO agent_definitions
		(name, display_name, description, system_prompt, agent_type,
		 profile, model, max_turns, tools, allowed_tools, disallowed_tools,
		 skills, auto_team, sub_agents, personality, triggers, source,
		 is_builtin, is_team_member, created_at, updated_at)
		 VALUES ('stale_scheduler','小时','','','sync','worker','',0,
		 '[]','[]','[]','[]',0,'[]','','','builtin',1,0,?,?)`,
		time.Now(), time.Now())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Also a row where only source='builtin' but is_builtin=0 — covers
	// the OR branch of the WHERE clause.
	_, err = store.db.Exec(`INSERT INTO agent_definitions
		(name, display_name, description, system_prompt, agent_type,
		 profile, model, max_turns, tools, allowed_tools, disallowed_tools,
		 skills, auto_team, sub_agents, personality, triggers, source,
		 is_builtin, is_team_member, created_at, updated_at)
		 VALUES ('source_only_builtin','','','','sync','worker','',0,
		 '[]','[]','[]','[]',0,'[]','','','builtin',0,0,?,?)`,
		time.Now(), time.Now())
	if err != nil {
		t.Fatalf("seed source-only: %v", err)
	}
	// Legitimate user-created record must survive the purge.
	if _, err := store.Create(context.Background(), &AgentDefinition{
		Name: "user_yaml", AgentType: "sync", Profile: "worker", Source: "yaml",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	n, err := store.PurgeBuiltins(context.Background())
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Errorf("PurgeBuiltins returned %d, want 2", n)
	}

	all, _ := store.List(context.Background(), nil)
	for _, d := range all {
		if d.Name == "stale_scheduler" || d.Name == "source_only_builtin" {
			t.Errorf("%s still present after PurgeBuiltins", d.Name)
		}
	}
	if len(all) != 1 || all[0].Name != "user_yaml" {
		t.Errorf("user record should survive purge, got %d records: %+v", len(all), all)
	}
}
