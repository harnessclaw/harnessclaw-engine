// Package fileedit implements the FileEdit (Edit) tool for precise string replacements.
package fileedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const toolName = "Edit"

// editInput is the JSON structure the LLM sends to invoke the tool.
type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll *bool  `json:"replace_all,omitempty"` // default false
}

// FileEditTool performs exact string replacements in files.
type FileEditTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a FileEditTool with the given config.
func New(cfg config.ToolConfig) *FileEditTool {
	return &FileEditTool{cfg: cfg}
}

func (t *FileEditTool) Name() string            { return toolName }
func (t *FileEditTool) Description() string     { return fileEditDescription }
func (t *FileEditTool) IsReadOnly() bool                { return false }
func (t *FileEditTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (t *FileEditTool) IsEnabled() bool         { return t.cfg.Enabled }

func (t *FileEditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "要修改文件的绝对路径。",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "要被替换的文本。",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "替换后的文本（必须与 old_string 不同）。",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "为 true 时替换文件中所有匹配（默认 false）。",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

func (t *FileEditTool) ValidateInput(input json.RawMessage) error {
	var ei editInput
	if err := json.Unmarshal(input, &ei); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if ei.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if ei.OldString == ei.NewString {
		return fmt.Errorf("old_string and new_string must be different")
	}
	return nil
}

func (t *FileEditTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var ei editInput
	if err := json.Unmarshal(input, &ei); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Read file.
	content, err := os.ReadFile(ei.FilePath)
	if err != nil {
		return &types.ToolResult{Content: "error reading file: " + err.Error(), IsError: true}, nil
	}

	fileContent := string(content)
	replaceAll := ei.ReplaceAll != nil && *ei.ReplaceAll

	// Check uniqueness (unless replace_all is true).
	if !replaceAll {
		count := strings.Count(fileContent, ei.OldString)
		if count == 0 {
			return &types.ToolResult{
				Content: fmt.Sprintf("old_string not found in %s. Make sure it matches exactly, including whitespace and indentation.", ei.FilePath),
				IsError: true,
			}, nil
		}
		if count > 1 {
			return &types.ToolResult{
				Content: fmt.Sprintf("old_string found %d times in %s. Provide more context to make it unique, or use replace_all.", count, ei.FilePath),
				IsError: true,
			}, nil
		}
	} else {
		if !strings.Contains(fileContent, ei.OldString) {
			return &types.ToolResult{
				Content: fmt.Sprintf("old_string not found in %s.", ei.FilePath),
				IsError: true,
			}, nil
		}
	}

	// Perform replacement.
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(fileContent, ei.OldString, ei.NewString)
	} else {
		newContent = strings.Replace(fileContent, ei.OldString, ei.NewString, 1)
	}

	// Write back.
	info, _ := os.Stat(ei.FilePath)
	perm := os.FileMode(0644)
	if info != nil {
		perm = info.Mode()
	}
	if err := os.WriteFile(ei.FilePath, []byte(newContent), perm); err != nil {
		return &types.ToolResult{Content: "error writing file: " + err.Error(), IsError: true}, nil
	}

	return &types.ToolResult{
		Content: fmt.Sprintf("Successfully edited %s", ei.FilePath),
		Metadata: map[string]any{
			"render_hint": "diff",
			"file_path":   ei.FilePath,
			"language":    tool.ExtToLanguage(filepath.Ext(ei.FilePath)),
		},
	}, nil
}

const fileEditDescription = `在文件中做精确字符串替换。

使用规范：
- old_string 在文件中不唯一时，编辑会失败。要么补足够上下文让它唯一，要么用 replace_all。
- 全局重命名 / 替换用 replace_all。
- old_string 与 new_string 必须不同。`
