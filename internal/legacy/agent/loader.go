package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"harnessclaw-go/internal/tools"
)

// LoadFromDirectory discovers and loads agent definitions from YAML files
// in the given directory (e.g., ".harnessclaw/agents/"). Files must have
// .yaml or .yml extension. dir may originate from operator-supplied input
// (see AgentService.ImportFromYAML / POST /console/v1/agents/import), so
// every read is performed through os.Root, which constrains all file
// operations to the resolved directory and refuses any name that would
// escape it.
func (r *AgentDefinitionRegistry) LoadFromDirectory(dir string) error {
	cleanDir := filepath.Clean(dir)
	root, err := os.OpenRoot(cleanDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory doesn't exist — not an error
		}
		return fmt.Errorf("open agent definitions dir: %w", err)
	}
	defer root.Close()

	rootDir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open agent definitions dir: %w", err)
	}
	entries, err := rootDir.ReadDir(-1)
	rootDir.Close()
	if err != nil {
		return fmt.Errorf("read agent definitions dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		data, err := readFileWithinRoot(root, name)
		if err != nil {
			return fmt.Errorf("load agent definition %s: %w", name, err)
		}
		def, err := parseAgentDefinition(data)
		if err != nil {
			return fmt.Errorf("load agent definition %s: %w", name, err)
		}
		path := filepath.Join(cleanDir, name)
		def.Source = path
		if err := r.Register(def); err != nil {
			return fmt.Errorf("register agent definition %s: %w", path, err)
		}
	}
	return nil
}

// readFileWithinRoot reads name relative to root. os.Root rejects any name
// containing parent traversal or absolute path segments.
func readFileWithinRoot(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
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
	Skills          []string       `yaml:"skills"`
	AutoTeam        bool           `yaml:"auto_team"`
	SubAgents       []yamlSubAgent `yaml:"sub_agents"`
}

type yamlSubAgent struct {
	Name      string `yaml:"name"`
	Role      string `yaml:"role"`
	AgentType string `yaml:"agent_type"`
	Profile   string `yaml:"profile"`
}

func parseAgentDefinition(data []byte) (*AgentDefinition, error) {
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
		Skills:          raw.Skills,
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
