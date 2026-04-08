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

func (t *FileWriteTool) Name() string            { return toolName }
func (t *FileWriteTool) Description() string     { return fileWriteDescription }
func (t *FileWriteTool) IsReadOnly() bool        { return false }
func (t *FileWriteTool) IsEnabled() bool         { return t.cfg.Enabled }

func (t *FileWriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to write (must be absolute, not relative)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
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
	return nil
}

func (t *FileWriteTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(wi.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &types.ToolResult{Content: "error creating directory: " + err.Error(), IsError: true}, nil
	}

	// Preserve existing permissions if file exists.
	perm := os.FileMode(0644)
	if info, err := os.Stat(wi.FilePath); err == nil {
		perm = info.Mode()
	}

	if err := os.WriteFile(wi.FilePath, []byte(wi.Content), perm); err != nil {
		return &types.ToolResult{Content: "error writing file: " + err.Error(), IsError: true}, nil
	}

	return &types.ToolResult{Content: fmt.Sprintf("Successfully wrote to %s", wi.FilePath)}, nil
}

const fileWriteDescription = `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- The file_path parameter must be an absolute path, not a relative path.
- Parent directories will be created automatically if they don't exist.`
