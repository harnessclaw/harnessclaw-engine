package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harnessclaw-go/internal/engine/agent/builtin/browser_agent/resources"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const (
	SkillReferenceToolName = "browser_skill_reference"
	maxReferenceBytes      = 20_000
)

type SkillReferenceTool struct {
	tool.BaseTool
	cfg       config.BrowserAgentConfig
	root      string
	readEmbed func(string) ([]byte, error)
}

type referenceInput struct {
	Path string `json:"path"`
}

func NewSkillReferenceTool(cfg config.BrowserAgentConfig) *SkillReferenceTool {
	return &SkillReferenceTool{cfg: cfg, readEmbed: browseragentresources.ReadReference}
}

func NewSkillReferenceToolForTest(cfg config.BrowserAgentConfig, root string) *SkillReferenceTool {
	return &SkillReferenceTool{cfg: cfg, root: root}
}

func (t *SkillReferenceTool) Name() string { return SkillReferenceToolName }
func (t *SkillReferenceTool) Description() string {
	return "按需读取 embedded agent-browser skill 的受控 reference 或 template 文件。仅在主 SKILL.md 信息不足时使用。"
}
func (t *SkillReferenceTool) IsReadOnly() bool              { return true }
func (t *SkillReferenceTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *SkillReferenceTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }

func (t *SkillReferenceTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": `受控相对路径，例如 "references/session-management.md"。只允许 references/*.md 和 templates/*.sh。`,
				"minLength":   1,
			},
		},
		"required": []string{"path"},
	}
}

func (t *SkillReferenceTool) ValidateInput(raw json.RawMessage) error {
	var in referenceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_skill_reference input: %w", err)
	}
	_, err := cleanReferencePath(in.Path)
	return err
}

func (t *SkillReferenceTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in referenceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return invalidReferenceResult(err.Error()), nil
	}
	rel, err := cleanReferencePath(in.Path)
	if err != nil {
		return invalidReferenceResult(err.Error()), nil
	}

	body, err := t.readReference(rel)
	if err != nil {
		return &types.ToolResult{
			Content:   fmt.Sprintf("browser_skill_reference read failed at %s: %v", rel, err),
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}, nil
	}
	content := string(body)
	truncated := false
	if len(content) > maxReferenceBytes {
		content = content[:maxReferenceBytes]
		truncated = true
	}
	return &types.ToolResult{
		Content: content,
		Metadata: map[string]any{
			"path":             rel,
			"truncated":        truncated,
			"max_output_bytes": maxReferenceBytes,
		},
	}, nil
}

func cleanReferencePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("path must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(trimmed))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("path must stay inside embedded agent-browser skill")
	}
	dir, base := filepath.Split(clean)
	dir = strings.TrimSuffix(dir, "/")
	switch dir {
	case "references":
		if strings.HasSuffix(base, ".md") && base != ".md" {
			return clean, nil
		}
	case "templates":
		if strings.HasSuffix(base, ".sh") && base != ".sh" {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path must match references/*.md or templates/*.sh")
}

func isWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(filepath.ToSlash(rel), "../")
}

func invalidReferenceResult(msg string) *types.ToolResult {
	return &types.ToolResult{
		Content:   msg,
		IsError:   true,
		ErrorType: types.ToolErrorInvalidInput,
	}
}

func (t *SkillReferenceTool) readReference(rel string) ([]byte, error) {
	if t.readEmbed != nil {
		return t.readEmbed(rel)
	}
	root := strings.TrimSpace(t.root)
	if root == "" {
		return nil, fmt.Errorf("browser_skill_reference root is not configured")
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !isWithinRoot(root, abs) {
		return nil, fmt.Errorf("path escapes agent-browser skill root")
	}
	return os.ReadFile(abs)
}
