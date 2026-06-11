package browseragentresources

import (
	"embed"
	"fmt"
	"strings"
)

const (
	SkillPath = "embedded://agent-browser/SKILL.md"
	skillFile = "agent-browser/SKILL.md"
)

//go:embed agent-browser/SKILL.md agent-browser/references/*.md agent-browser/templates/*.sh
var files embed.FS

func SkillBody() (string, error) {
	body, err := files.ReadFile(skillFile)
	if err != nil {
		return "", fmt.Errorf("read embedded agent-browser skill: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func ReadReference(rel string) ([]byte, error) {
	path := "agent-browser/" + strings.TrimPrefix(rel, "/")
	body, err := files.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read embedded agent-browser reference %s: %w", rel, err)
	}
	return body, nil
}
