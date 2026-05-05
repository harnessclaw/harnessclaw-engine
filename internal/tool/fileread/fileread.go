// Package fileread implements the FileRead (Read) tool for reading file contents.
package fileread

import (
	"bufio"
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

const toolName = "Read"

// readInput is the JSON structure the LLM sends to invoke the tool.
type readInput struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset,omitempty"` // 1-based line number to start from
	Limit    *int   `json:"limit,omitempty"`  // number of lines to read
}

// FileReadTool reads files and returns their contents with line numbers.
type FileReadTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a FileReadTool with the given config.
func New(cfg config.ToolConfig) *FileReadTool {
	return &FileReadTool{cfg: cfg}
}

func (t *FileReadTool) Name() string                   { return toolName }
func (t *FileReadTool) Description() string            { return fileReadDescription }
func (t *FileReadTool) IsReadOnly() bool                   { return true }
func (t *FileReadTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *FileReadTool) IsConcurrencySafe() bool        { return true }
func (t *FileReadTool) IsEnabled() bool                { return t.cfg.Enabled }

func (t *FileReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "要读取文件的绝对路径。",
			},
			"offset": map[string]any{
				"type":        "number",
				"description": "起始行号。仅在文件过大、需要读指定区段时使用。",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "要读取的行数。仅在文件过大、需要读指定区段时使用。",
			},
		},
		"required": []string{"file_path"},
	}
}

func (t *FileReadTool) ValidateInput(input json.RawMessage) error {
	var ri readInput
	if err := json.Unmarshal(input, &ri); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if ri.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	return nil
}

func (t *FileReadTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var ri readInput
	if err := json.Unmarshal(input, &ri); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Check file exists.
	info, err := os.Stat(ri.FilePath)
	if err != nil {
		return &types.ToolResult{Content: fmt.Sprintf("file not found: %s", ri.FilePath), IsError: true}, nil
	}
	if info.IsDir() {
		return &types.ToolResult{Content: fmt.Sprintf("%s is a directory, not a file. Use ls via Bash to list directory contents.", ri.FilePath), IsError: true}, nil
	}

	// Default limits.
	offset := 1
	limit := 2000
	if ri.Offset != nil && *ri.Offset > 0 {
		offset = *ri.Offset
	}
	if ri.Limit != nil && *ri.Limit > 0 {
		limit = *ri.Limit
	}

	// Read file with line numbers (cat -n format).
	f, err := os.Open(ri.FilePath)
	if err != nil {
		return &types.ToolResult{Content: "error opening file: " + err.Error(), IsError: true}, nil
	}
	defer f.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	lineNum := 0
	linesRead := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < offset {
			continue
		}
		if linesRead >= limit {
			break
		}
		sb.WriteString(fmt.Sprintf("%6d\t%s\n", lineNum, scanner.Text()))
		linesRead++
	}

	if err := scanner.Err(); err != nil {
		return &types.ToolResult{Content: "error reading file: " + err.Error(), IsError: true}, nil
	}

	if sb.Len() == 0 {
		return &types.ToolResult{
			Content: "(empty file)",
			Metadata: map[string]any{
				"render_hint": "code",
				"file_path":   ri.FilePath,
				"language":    tool.ExtToLanguage(filepath.Ext(ri.FilePath)),
			},
		}, nil
	}

	return &types.ToolResult{
		Content: sb.String(),
		Metadata: map[string]any{
			"render_hint": "code",
			"file_path":   ri.FilePath,
			"language":    tool.ExtToLanguage(filepath.Ext(ri.FilePath)),
			"start_line":  offset,
			"lines_read":  linesRead,
		},
	}, nil
}

const fileReadDescription = `读取本地文件系统中的文件。可以直接访问任意文件。

使用规范：
- file_path 必须是绝对路径，不能相对路径。
- 默认从文件开头读最多 2000 行。
- 文件较大时用 offset 和 limit 读指定区段。
- 返回结果按 cat -n 风格，行号从 1 开始。`
