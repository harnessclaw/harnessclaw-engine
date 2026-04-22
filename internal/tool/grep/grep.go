// Package grep implements the Grep tool for searching file contents via ripgrep.
package grep

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	toolName      = "Grep"
	maxOutputLen  = 30_000
)

// grepInput is the JSON structure the LLM sends to invoke the tool.
type grepInput struct {
	Pattern    string  `json:"pattern"`
	Path       *string `json:"path,omitempty"`
	Glob       *string `json:"glob,omitempty"`       // file glob filter
	Type       *string `json:"type,omitempty"`       // file type (js, py, go, etc.)
	OutputMode *string `json:"output_mode,omitempty"` // content, files_with_matches, count
	Context    *int    `json:"context,omitempty"`     // context lines (-C)
	Before     *int    `json:"-B,omitempty"`
	After      *int    `json:"-A,omitempty"`
	IgnoreCase *bool   `json:"-i,omitempty"`
	LineNums   *bool   `json:"-n,omitempty"`
	Multiline  *bool   `json:"multiline,omitempty"`
	HeadLimit  *int    `json:"head_limit,omitempty"`
}

// GrepTool searches file contents using ripgrep.
type GrepTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a GrepTool with the given config.
func New(cfg config.ToolConfig) *GrepTool {
	return &GrepTool{cfg: cfg}
}

func (t *GrepTool) Name() string                   { return toolName }
func (t *GrepTool) Description() string            { return grepDescription }
func (t *GrepTool) IsReadOnly() bool               { return true }
func (t *GrepTool) IsConcurrencySafe() bool        { return true }
func (t *GrepTool) IsEnabled() bool                { return t.cfg.Enabled }

func (t *GrepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regular expression pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to search in. Defaults to current working directory.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Glob pattern to filter files (e.g. \"*.js\", \"*.{ts,tsx}\")",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "File type to search (rg --type). Common types: js, py, go, java, etc.",
			},
			"output_mode": map[string]any{
				"type":        "string",
				"description": "Output mode: content, files_with_matches (default), count",
				"enum":        []string{"content", "files_with_matches", "count"},
			},
			"context": map[string]any{
				"type":        "number",
				"description": "Context lines around matches (rg -C)",
			},
			"multiline": map[string]any{
				"type":        "boolean",
				"description": "Enable multiline matching (rg -U)",
			},
			"head_limit": map[string]any{
				"type":        "number",
				"description": "Limit output to first N entries",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) ValidateInput(input json.RawMessage) error {
	var gi grepInput
	if err := json.Unmarshal(input, &gi); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if gi.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	return nil
}

func (t *GrepTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var gi grepInput
	if err := json.Unmarshal(input, &gi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Build ripgrep command.
	args := buildRgArgs(&gi)

	cmd := exec.CommandContext(ctx, "rg", args...)
	if gi.Path != nil && *gi.Path != "" {
		// rg searches in the specified path
	}

	out, err := cmd.CombinedOutput()
	output := string(out)

	// rg exits 1 when no matches found — not an error.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &types.ToolResult{
				Content: "No matches found.",
				Metadata: map[string]any{
					"render_hint": "search",
					"pattern":     gi.Pattern,
					"match_count": 0,
				},
			}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return &types.ToolResult{Content: "Error: " + output, IsError: true}, nil
		}
		// Context cancelled.
		if ctx.Err() != nil {
			return &types.ToolResult{Content: "search cancelled", IsError: true}, nil
		}
	}

	// Truncate if too long.
	if len(output) > maxOutputLen {
		output = output[:maxOutputLen] + "\n... (output truncated)"
	}

	// Apply head_limit.
	if gi.HeadLimit != nil && *gi.HeadLimit > 0 {
		lines := strings.Split(output, "\n")
		if len(lines) > *gi.HeadLimit {
			output = strings.Join(lines[:*gi.HeadLimit], "\n")
		}
	}

	return &types.ToolResult{
		Content: output,
		Metadata: map[string]any{
			"render_hint": "search",
			"pattern":     gi.Pattern,
			"match_count": countNonEmptyLines(output),
			"output_mode": gi.OutputMode,
		},
	}, nil
}

// countNonEmptyLines counts non-empty lines in output for match estimation.
func countNonEmptyLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// buildRgArgs constructs the ripgrep command-line arguments.
func buildRgArgs(gi *grepInput) []string {
	var args []string

	// Output mode.
	mode := "files_with_matches"
	if gi.OutputMode != nil {
		mode = *gi.OutputMode
	}

	switch mode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	case "content":
		args = append(args, "-n") // show line numbers
	}

	// Context lines.
	if gi.Context != nil && *gi.Context > 0 {
		args = append(args, fmt.Sprintf("-C%d", *gi.Context))
	}
	if gi.Before != nil && *gi.Before > 0 {
		args = append(args, fmt.Sprintf("-B%d", *gi.Before))
	}
	if gi.After != nil && *gi.After > 0 {
		args = append(args, fmt.Sprintf("-A%d", *gi.After))
	}

	// Case insensitive.
	if gi.IgnoreCase != nil && *gi.IgnoreCase {
		args = append(args, "-i")
	}

	// Multiline.
	if gi.Multiline != nil && *gi.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}

	// File type filter.
	if gi.Type != nil && *gi.Type != "" {
		args = append(args, "--type", *gi.Type)
	}

	// Glob filter.
	if gi.Glob != nil && *gi.Glob != "" {
		args = append(args, "--glob", *gi.Glob)
	}

	// Pattern.
	args = append(args, gi.Pattern)

	// Path.
	if gi.Path != nil && *gi.Path != "" {
		args = append(args, *gi.Path)
	} else {
		args = append(args, ".")
	}

	return args
}

const grepDescription = `A powerful search tool built on ripgrep.

Usage:
- Supports full regex syntax (e.g., "log.*Error", "function\\s+\\w+")
- Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Use multiline: true for patterns spanning multiple lines`
