package agent

import "testing"

func TestTeamManager_Create(t *testing.T) {
	tm := NewTeamManager()
	team, err := tm.Create("my-project", "Working on feature X", "leader")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if team.Name != "my-project" {
		t.Errorf("expected name 'my-project', got %q", team.Name)
	}
	if team.LeaderName != "leader" {
		t.Errorf("expected leader 'leader', got %q", team.LeaderName)
	}
}

func TestTeamManager_Create_Duplicate(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_, err := tm.Create("proj", "", "lead")
	if err == nil {
		t.Error("expected error for duplicate team name")
	}
}

func TestTeamManager_Get(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")

	team := tm.Get("proj")
	if team == nil {
		t.Fatal("expected to find team")
	}

	if tm.Get("nonexistent") != nil {
		t.Error("expected nil for unknown team")
	}
}

func TestTeamManager_Delete(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")

	err := tm.Delete("proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tm.Get("proj") != nil {
		t.Error("expected team to be deleted")
	}
}

func TestTeamManager_Delete_ActiveMembers(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_ = tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a1", Status: "active"})

	err := tm.Delete("proj")
	if err == nil {
		t.Error("expected error when deleting team with active members")
	}
}

func TestTeamManager_Delete_NotFound(t *testing.T) {
	tm := NewTeamManager()
	err := tm.Delete("nonexistent")
	if err == nil {
		t.Error("expected error for unknown team")
	}
}

func TestTeamManager_AddMember(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")

	err := tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a1", AgentType: "sync", Status: "active"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	team := tm.Get("proj")
	if len(team.Members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(team.Members))
	}
	if team.Members[0].Name != "worker" {
		t.Errorf("expected member name 'worker', got %q", team.Members[0].Name)
	}
}

func TestTeamManager_AddMember_Duplicate(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_ = tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a1", Status: "active"})

	err := tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a2", Status: "active"})
	if err == nil {
		t.Error("expected error for duplicate member name")
	}
}

func TestTeamManager_RemoveMember(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_ = tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a1", Status: "idle"})

	err := tm.RemoveMember("proj", "worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	team := tm.Get("proj")
	if len(team.Members) != 0 {
		t.Errorf("expected 0 members after removal, got %d", len(team.Members))
	}
}

func TestTeamManager_RemoveMember_NotFound(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")

	err := tm.RemoveMember("proj", "ghost")
	if err == nil {
		t.Error("expected error for unknown member")
	}
}

func TestTeamManager_SetMemberStatus(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj", "", "lead")
	_ = tm.AddMember("proj", TeamMember{Name: "worker", AgentID: "a1", Status: "active"})

	err := tm.SetMemberStatus("proj", "worker", "idle")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	team := tm.Get("proj")
	if team.Members[0].Status != "idle" {
		t.Errorf("expected status 'idle', got %q", team.Members[0].Status)
	}
}

func TestTeamManager_ListTeams(t *testing.T) {
	tm := NewTeamManager()
	_, _ = tm.Create("proj-a", "", "lead")
	_, _ = tm.Create("proj-b", "", "lead")

	teams := tm.ListTeams()
	if len(teams) != 2 {
		t.Errorf("expected 2 teams, got %d", len(teams))
	}
}
