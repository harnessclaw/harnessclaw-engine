package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"harnessclaw-go/internal/tool"
)

// LoadFromDirectory discovers and loads agent definitions from YAML files
// in the given directory (e.g., ".harnessclaw/agents/"). Files must have
// .yaml or .yml extension.
func (r *AgentDefinitionRegistry) LoadFromDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory doesn't exist — not an error
		}
		return fmt.Errorf("read agent definitions dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		def, err := loadAgentDefinitionFile(path)
		if err != nil {
			return fmt.Errorf("load agent definition %s: %w", path, err)
		}
		def.Source = path
		r.Register(def)
	}
	return nil
}

// yamlAgentDef is the on-disk YAML representation of an agent definition.
type yamlAgentDef struct {
	Name            string         `yaml:"name"`
	DisplayName     string         `yaml:"display_name"`
	Description     string         `yaml:"description"`
	SystemPrompt    string         `yaml:"system_prompt"`
	AgentType       string         `yaml:"agent_type"`
	Profile         string         `yaml:"profile"`
	Model           string         `yaml:"model"`
	MaxTurns        int            `yaml:"max_turns"`
	Tools           []string       `yaml:"tools"`
	AllowedTools    []string       `yaml:"allowed_tools"`
	DisallowedTools []string       `yaml:"disallowed_tools"`
	AutoTeam        bool           `yaml:"auto_team"`
	SubAgents       []yamlSubAgent `yaml:"sub_agents"`
}

type yamlSubAgent struct {
	Name      string `yaml:"name"`
	Role      string `yaml:"role"`
	AgentType string `yaml:"agent_type"`
	Profile   string `yaml:"profile"`
}

func loadAgentDefinitionFile(path string) (*AgentDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw yamlAgentDef
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	if raw.Name == "" {
		return nil, fmt.Errorf("agent definition missing required 'name' field")
	}

	def := &AgentDefinition{
		Name:            raw.Name,
		DisplayName:     raw.DisplayName,
		Description:     raw.Description,
		SystemPrompt:    raw.SystemPrompt,
		AgentType:       parseAgentType(raw.AgentType),
		Profile:         raw.Profile,
		Model:           raw.Model,
		MaxTurns:        raw.MaxTurns,
		Tools:           raw.Tools,
		AllowedTools:    raw.AllowedTools,
		DisallowedTools: raw.DisallowedTools,
		AutoTeam:        raw.AutoTeam,
	}

	for _, sa := range raw.SubAgents {
		def.SubAgents = append(def.SubAgents, SubAgentDef{
			Name:      sa.Name,
			Role:      sa.Role,
			AgentType: parseAgentType(sa.AgentType),
			Profile:   sa.Profile,
		})
	}

	return def, nil
}

func parseAgentType(s string) tool.AgentType {
	switch s {
	case "sync":
		return tool.AgentTypeSync
	case "async":
		return tool.AgentTypeAsync
	case "teammate":
		return tool.AgentTypeTeammate
	case "coordinator":
		return tool.AgentTypeCoordinator
	case "custom":
		return tool.AgentTypeCustom
	default:
		return tool.AgentTypeSync
	}
}
