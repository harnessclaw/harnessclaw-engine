package agent

import (
	"fmt"
	"sync"
	"time"
)

// TeamMember describes a member of a team.
type TeamMember struct {
	Name      string `json:"name"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	Status    string `json:"status"` // "active", "idle", "shutdown"
}

// Team represents a group of agents working together.
type Team struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	LeaderName  string        `json:"leader_name"`
	Members     []TeamMember  `json:"members"`
	CreatedAt   time.Time     `json:"created_at"`
}

// TeamManager handles team lifecycle and coordination.
type TeamManager struct {
	mu    sync.RWMutex
	teams map[string]*Team // teamName -> team
}

// NewTeamManager creates a new team manager.
func NewTeamManager() *TeamManager {
	return &TeamManager{
		teams: make(map[string]*Team),
	}
}

// Create creates a new team. Returns error if team name already exists.
func (tm *TeamManager) Create(name, description, leaderName string) (*Team, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, ok := tm.teams[name]; ok {
		return nil, fmt.Errorf("team %q already exists", name)
	}

	team := &Team{
		ID:          "team_" + name,
		Name:        name,
		Description: description,
		LeaderName:  leaderName,
		Members:     []TeamMember{},
		CreatedAt:   time.Now(),
	}
	tm.teams[name] = team
	return team, nil
}

// Get returns a team by name, or nil if not found.
func (tm *TeamManager) Get(name string) *Team {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.teams[name]
}

// Delete removes a team. Returns error if team has active members.
func (tm *TeamManager) Delete(name string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	team, ok := tm.teams[name]
	if !ok {
		return fmt.Errorf("team %q not found", name)
	}

	for _, m := range team.Members {
		if m.Status == "active" {
			return fmt.Errorf("team %q has active member %q; shut down all members first", name, m.Name)
		}
	}

	delete(tm.teams, name)
	return nil
}

// AddMember adds a member to a team.
func (tm *TeamManager) AddMember(teamName string, member TeamMember) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	team, ok := tm.teams[teamName]
	if !ok {
		return fmt.Errorf("team %q not found", teamName)
	}

	// Check for duplicate name.
	for _, m := range team.Members {
		if m.Name == member.Name {
			return fmt.Errorf("member %q already exists in team %q", member.Name, teamName)
		}
	}

	team.Members = append(team.Members, member)
	return nil
}

// RemoveMember removes a member from a team.
func (tm *TeamManager) RemoveMember(teamName, memberName string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	team, ok := tm.teams[teamName]
	if !ok {
		return fmt.Errorf("team %q not found", teamName)
	}

	for i, m := range team.Members {
		if m.Name == memberName {
			team.Members = append(team.Members[:i], team.Members[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("member %q not found in team %q", memberName, teamName)
}

// SetMemberStatus updates a member's status in a team.
func (tm *TeamManager) SetMemberStatus(teamName, memberName, status string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	team, ok := tm.teams[teamName]
	if !ok {
		return fmt.Errorf("team %q not found", teamName)
	}

	for i := range team.Members {
		if team.Members[i].Name == memberName {
			team.Members[i].Status = status
			return nil
		}
	}
	return fmt.Errorf("member %q not found in team %q", memberName, teamName)
}

// ListTeams returns all teams.
func (tm *TeamManager) ListTeams() []*Team {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]*Team, 0, len(tm.teams))
	for _, t := range tm.teams {
		result = append(result, t)
	}
	return result
}
