// Package filewrite implements the FileWrite (Write) tool for creating/overwriting files.
package filewrite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const toolName = "Write"

// writeInput is the JSON structure the LLM sends to invoke the tool.
type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// FileWriteTool writes complete file contents.
type FileWriteTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a FileWriteTool with the given config.
func New(cfg config.ToolConfig) *FileWriteTool {
	return &FileWriteTool{cfg: cfg}
}

func (t *FileWriteTool) Name() string        { return toolName }
func (t *FileWriteTool) Description() string { return fileWriteDescription }
func (t *FileWriteTool) IsReadOnly() bool                { return false }
func (t *FileWriteTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (t *FileWriteTool) IsEnabled() bool     { return t.cfg.Enabled }

func (t *FileWriteTool) InputSchema() map[string]any {
	// Build default working directory path with cross-platform support
	defaultDir := getDefaultWorkingDir()
	filePathDesc := fmt.Sprintf(
		"要写入文件的绝对路径（必须绝对，不能相对）。未指定具体位置时，可用 %s 作为默认工作目录。",
		defaultDir,
	)

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": filePathDesc,
			},
			"content": map[string]any{
				"type":        "string",
				"description": "要写入文件的内容。",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (t *FileWriteTool) ValidateInput(input json.RawMessage) error {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if wi.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if !filepath.IsAbs(wi.FilePath) {
		return fmt.Errorf("file_path must be an absolute path")
	}
	if wi.Content == "" {
		return fmt.Errorf("content is required")
	}
	return nil
}

func (t *FileWriteTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	content := wi.Content
	if content == "" {
		return &types.ToolResult{Content: "content is required", IsError: true}, nil
	}

	// Verify target directory exists.
	dir := filepath.Dir(wi.FilePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return &types.ToolResult{Content: fmt.Sprintf("directory %s does not exist; create it first", dir), IsError: true}, nil
	}

	// Preserve existing permissions if file exists.
	perm := os.FileMode(0644)
	if info, err := os.Stat(wi.FilePath); err == nil {
		perm = info.Mode()
	}

	if err := os.WriteFile(wi.FilePath, []byte(content), perm); err != nil {
		return &types.ToolResult{Content: "error writing file: " + err.Error(), IsError: true}, nil
	}

	return &types.ToolResult{
		Content: fmt.Sprintf("Successfully wrote to %s", wi.FilePath),
		Metadata: map[string]any{
			"render_hint":   "file_info",
			"file_path":     wi.FilePath,
			"language":      tool.ExtToLanguage(filepath.Ext(wi.FilePath)),
			"bytes_written": len(content),
		},
	}, nil
}

const fileWriteDescription = `把内容写入本地文件系统。

使用规范：
- 目标路径已存在文件时会被覆盖。
- file_path 必须是绝对路径，不能相对路径。
- 写入前必须确保目标目录存在；不存在请用 Bash 先创建。`

// getDefaultWorkingDir returns the expanded default working directory path
// for file operations, with cross-platform support.
func getDefaultWorkingDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to literal path if home dir cannot be determined
		return "~/.harnessclaw/files/"
	}
	return filepath.Join(homeDir, ".harnessclaw", "files")
}
