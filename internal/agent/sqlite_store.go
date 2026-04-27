package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteAgentStore is a persistent AgentStore backed by SQLite.
type SQLiteAgentStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteAgentStore opens (or creates) a SQLite database at the given path
// and creates the agent_definitions table if it doesn't exist.
func NewSQLiteAgentStore(dbPath string) (*SQLiteAgentStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	const ddl = `CREATE TABLE IF NOT EXISTS agent_definitions (
		name              TEXT PRIMARY KEY,
		display_name      TEXT NOT NULL DEFAULT '',
		description       TEXT NOT NULL DEFAULT '',
		system_prompt     TEXT NOT NULL DEFAULT '',
		agent_type        TEXT NOT NULL DEFAULT 'sync',
		profile           TEXT NOT NULL DEFAULT '',
		model             TEXT NOT NULL DEFAULT '',
		max_turns         INTEGER NOT NULL DEFAULT 0,
		tools             TEXT NOT NULL DEFAULT '[]',
		allowed_tools     TEXT NOT NULL DEFAULT '[]',
		disallowed_tools  TEXT NOT NULL DEFAULT '[]',
		skills            TEXT NOT NULL DEFAULT '[]',
		auto_team         INTEGER NOT NULL DEFAULT 0,
		sub_agents        TEXT NOT NULL DEFAULT '[]',
		personality       TEXT NOT NULL DEFAULT '',
		triggers          TEXT NOT NULL DEFAULT '',
		source            TEXT NOT NULL DEFAULT 'custom',
		is_builtin        INTEGER NOT NULL DEFAULT 0,
		is_team_member    INTEGER NOT NULL DEFAULT 0,
		is_deleted        INTEGER NOT NULL DEFAULT 0,
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL
	)`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	// Migrate: add columns for existing databases that lack them.
	migrations := []string{
		`ALTER TABLE agent_definitions ADD COLUMN triggers TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_definitions ADD COLUMN is_team_member INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agent_definitions ADD COLUMN personality TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		_, _ = db.Exec(m) // ignore "duplicate column" errors
	}

	return &SQLiteAgentStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteAgentStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteAgentStore) Create(_ context.Context, def *AgentDefinition) (*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	toolsJSON, _ := json.Marshal(def.Tools)
	allowedJSON, _ := json.Marshal(def.AllowedTools)
	disallowedJSON, _ := json.Marshal(def.DisallowedTools)
	skillsJSON, _ := json.Marshal(def.Skills)
	subAgentsJSON, _ := json.Marshal(def.SubAgents)

	autoTeam := 0
	if def.AutoTeam {
		autoTeam = 1
	}
	isBuiltin := 0
	if def.IsBuiltin {
		isBuiltin = 1
	}
	isTeamMember := 0
	if def.IsTeamMember {
		isTeamMember = 1
	}

	source := def.Source
	if source == "" {
		source = "custom"
	}

	_, err := s.db.Exec(
		`INSERT INTO agent_definitions (name, display_name, description, system_prompt, agent_type,
		 profile, model, max_turns, tools, allowed_tools, disallowed_tools, skills, auto_team,
		 sub_agents, personality, triggers, source, is_builtin, is_team_member, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		def.Name, def.DisplayName, def.Description, def.SystemPrompt,
		string(def.AgentType), def.Profile, def.Model, def.MaxTurns,
		string(toolsJSON), string(allowedJSON), string(disallowedJSON),
		string(skillsJSON), autoTeam, string(subAgentsJSON), def.Personality, def.Triggers,
		source, isBuiltin, isTeamMember,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert agent definition: %w", err)
	}

	cp := *def
	cp.Source = source
	return &cp, nil
}

func (s *SQLiteAgentStore) Get(_ context.Context, name string) (*AgentDefinition, error) {
	row := s.db.QueryRow(
		`SELECT display_name, description, system_prompt, agent_type, profile, model,
		 max_turns, tools, allowed_tools, disallowed_tools, skills, auto_team,
		 sub_agents, personality, triggers, source, is_builtin, is_team_member
		 FROM agent_definitions WHERE name = ? AND is_deleted = 0`,
		name,
	)
	return scanAgentDef(name, row)
}

func (s *SQLiteAgentStore) List(_ context.Context, filter *AgentFilter) ([]*AgentDefinition, error) {
	query := `SELECT name, display_name, description, system_prompt, agent_type, profile, model,
		 max_turns, tools, allowed_tools, disallowed_tools, skills, auto_team,
		 sub_agents, personality, triggers, source, is_builtin, is_team_member
		 FROM agent_definitions`

	var args []any
	var whereClauses []string

	if filter != nil {
		if filter.AgentType != nil {
			whereClauses = append(whereClauses, "agent_type = ?")
			args = append(args, *filter.AgentType)
		}
		if filter.Source != nil {
			// Support prefix matching: "yaml" matches "yaml:/path/..."
			whereClauses = append(whereClauses, "source LIKE ?")
			args = append(args, *filter.Source+"%")
		}
	}

	// Always exclude soft-deleted records.
	whereClauses = append(whereClauses, "is_deleted = 0")

	query += " WHERE "
	for i, c := range whereClauses {
		if i > 0 {
			query += " AND "
		}
		query += c
	}

	query += " ORDER BY name"

	if filter != nil && filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent definitions: %w", err)
	}
	defer rows.Close()

	var result []*AgentDefinition
	for rows.Next() {
		var d AgentDefinition
		var toolsStr, allowedStr, disallowedStr, skillsStr, subAgentsStr string
		var autoTeamInt, isBuiltinInt, isTeamMemberInt int
		if err := rows.Scan(&d.Name, &d.DisplayName, &d.Description, &d.SystemPrompt,
			&d.AgentType, &d.Profile, &d.Model, &d.MaxTurns,
			&toolsStr, &allowedStr, &disallowedStr, &skillsStr,
			&autoTeamInt, &subAgentsStr, &d.Personality, &d.Triggers, &d.Source, &isBuiltinInt, &isTeamMemberInt); err != nil {
			return nil, fmt.Errorf("scan agent definition: %w", err)
		}
		d.AutoTeam = autoTeamInt != 0
		d.IsBuiltin = isBuiltinInt != 0
		d.IsTeamMember = isTeamMemberInt != 0
		_ = json.Unmarshal([]byte(toolsStr), &d.Tools)
		_ = json.Unmarshal([]byte(allowedStr), &d.AllowedTools)
		_ = json.Unmarshal([]byte(disallowedStr), &d.DisallowedTools)
		_ = json.Unmarshal([]byte(skillsStr), &d.Skills)
		_ = json.Unmarshal([]byte(subAgentsStr), &d.SubAgents)
		result = append(result, &d)
	}
	return result, nil
}

func (s *SQLiteAgentStore) Update(_ context.Context, name string, updates *AgentUpdate) (*AgentDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read current state.
	row := s.db.QueryRow(
		`SELECT display_name, description, system_prompt, agent_type, profile, model,
		 max_turns, tools, allowed_tools, disallowed_tools, skills, auto_team,
		 sub_agents, personality, triggers, source, is_builtin, is_team_member
		 FROM agent_definitions WHERE name = ? AND is_deleted = 0`,
		name,
	)
	d, err := scanAgentDef(name, row)
	if err != nil {
		return nil, err
	}

	// Apply updates.
	if updates.DisplayName != nil {
		d.DisplayName = *updates.DisplayName
	}
	if updates.Description != nil {
		d.Description = *updates.Description
	}
	if updates.SystemPrompt != nil {
		d.SystemPrompt = *updates.SystemPrompt
	}
	if updates.Model != nil {
		d.Model = *updates.Model
	}
	if updates.Profile != nil {
		d.Profile = *updates.Profile
	}
	if updates.MaxTurns != nil {
		d.MaxTurns = *updates.MaxTurns
	}
	if updates.Tools != nil {
		d.Tools = updates.Tools
	}
	if updates.AllowedTools != nil {
		d.AllowedTools = updates.AllowedTools
	}
	if updates.DisallowedTools != nil {
		d.DisallowedTools = updates.DisallowedTools
	}
	if updates.Skills != nil {
		d.Skills = updates.Skills
	}
	if updates.AutoTeam != nil {
		d.AutoTeam = *updates.AutoTeam
	}
	if updates.SubAgents != nil {
		d.SubAgents = updates.SubAgents
	}
	if updates.Personality != nil {
		d.Personality = *updates.Personality
	}
	if updates.Triggers != nil {
		d.Triggers = *updates.Triggers
	}
	if updates.IsTeamMember != nil {
		d.IsTeamMember = *updates.IsTeamMember
	}

	now := time.Now()
	toolsJSON, _ := json.Marshal(d.Tools)
	allowedJSON, _ := json.Marshal(d.AllowedTools)
	disallowedJSON, _ := json.Marshal(d.DisallowedTools)
	skillsJSON, _ := json.Marshal(d.Skills)
	subAgentsJSON, _ := json.Marshal(d.SubAgents)

	autoTeam := 0
	if d.AutoTeam {
		autoTeam = 1
	}

	isTeamMember := 0
	if d.IsTeamMember {
		isTeamMember = 1
	}

	_, err = s.db.Exec(
		`UPDATE agent_definitions SET display_name=?, description=?, system_prompt=?,
		 profile=?, model=?, max_turns=?, tools=?, allowed_tools=?, disallowed_tools=?,
		 skills=?, auto_team=?, sub_agents=?, personality=?, triggers=?, is_team_member=?, updated_at=?
		 WHERE name=?`,
		d.DisplayName, d.Description, d.SystemPrompt,
		d.Profile, d.Model, d.MaxTurns,
		string(toolsJSON), string(allowedJSON), string(disallowedJSON),
		string(skillsJSON), autoTeam, string(subAgentsJSON),
		d.Personality, d.Triggers, isTeamMember,
		now.Format(time.RFC3339Nano),
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("update agent definition: %w", err)
	}

	cp := *d
	return &cp, nil
}

func (s *SQLiteAgentStore) Delete(_ context.Context, name string) error {
	var isBuiltin int
	err := s.db.QueryRow("SELECT is_builtin FROM agent_definitions WHERE name = ? AND is_deleted = 0", name).Scan(&isBuiltin)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("agent definition %q not found", name)
		}
		return fmt.Errorf("lookup agent definition: %w", err)
	}
	if isBuiltin != 0 {
		return fmt.Errorf("cannot delete built-in agent definition %q", name)
	}

	res, err := s.db.Exec("DELETE FROM agent_definitions WHERE name = ? AND is_deleted = 0", name)
	if err != nil {
		return fmt.Errorf("delete agent definition: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent definition %q not found", name)
	}
	return nil
}

// scanAgentDef reads a single agent definition row into an AgentDefinition.
func scanAgentDef(name string, row *sql.Row) (*AgentDefinition, error) {
	var d AgentDefinition
	d.Name = name
	var toolsStr, allowedStr, disallowedStr, skillsStr, subAgentsStr string
	var autoTeamInt, isBuiltinInt, isTeamMemberInt int
	if err := row.Scan(&d.DisplayName, &d.Description, &d.SystemPrompt,
		&d.AgentType, &d.Profile, &d.Model, &d.MaxTurns,
		&toolsStr, &allowedStr, &disallowedStr, &skillsStr,
		&autoTeamInt, &subAgentsStr, &d.Personality, &d.Triggers, &d.Source, &isBuiltinInt, &isTeamMemberInt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("agent definition %q not found", name)
		}
		return nil, fmt.Errorf("scan agent definition: %w", err)
	}
	d.AutoTeam = autoTeamInt != 0
	d.IsBuiltin = isBuiltinInt != 0
	d.IsTeamMember = isTeamMemberInt != 0
	_ = json.Unmarshal([]byte(toolsStr), &d.Tools)
	_ = json.Unmarshal([]byte(allowedStr), &d.AllowedTools)
	_ = json.Unmarshal([]byte(disallowedStr), &d.DisallowedTools)
	_ = json.Unmarshal([]byte(skillsStr), &d.Skills)
	_ = json.Unmarshal([]byte(subAgentsStr), &d.SubAgents)
	return &d, nil
}
