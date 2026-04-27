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
	FilePath    string `json:"file_path"`
	Content     string `json:"content"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
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
func (t *FileWriteTool) IsReadOnly() bool    { return false }
func (t *FileWriteTool) IsEnabled() bool     { return t.cfg.Enabled }

func (t *FileWriteTool) InputSchema() map[string]any {
	// Build default working directory path with cross-platform support
	defaultDir := getDefaultWorkingDir()
	filePathDesc := fmt.Sprintf(
		"The absolute path to the file to write (must be absolute, not relative). If no specific location is mentioned, you may use %s as a default working directory.",
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
				"description": "The content to write to the file",
			},
			"artifact_ref": map[string]any{
				"type":        "string",
				"description": "Write content from a stored artifact instead of inline content. Pass the artifact ID (e.g., art_abc12345). When set, the content parameter is ignored.",
			},
		},
		"required": []string{"file_path"},
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
	if wi.Content == "" && wi.ArtifactRef == "" {
		return fmt.Errorf("either content or artifact_ref is required")
	}
	return nil
}

func (t *FileWriteTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Resolve content: artifact_ref takes priority over inline content.
	content := wi.Content
	if wi.ArtifactRef != "" {
		store, ok := tool.GetArtifactStore(ctx)
		if !ok {
			return &types.ToolResult{Content: "artifact store not available; provide content directly", IsError: true}, nil
		}
		art := store.Get(wi.ArtifactRef)
		if art.ID == "" {
			return &types.ToolResult{
				Content: fmt.Sprintf("artifact %q not found; provide content directly", wi.ArtifactRef),
				IsError: true,
			}, nil
		}
		content = art.Content
	}
	if content == "" {
		return &types.ToolResult{Content: "either content or artifact_ref is required", IsError: true}, nil
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

const fileWriteDescription = `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- The file_path parameter must be an absolute path, not a relative path.
- You must ensure the target directory exists before writing. Use Bash to create it if needed.
- Use artifact_ref to write content from a stored artifact instead of providing inline content.
  This avoids re-generating large content the model has already produced.`

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
