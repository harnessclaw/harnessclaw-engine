// Package glob implements the Glob tool for fast file pattern matching.
package glob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	toolName     = "Glob"
	maxResults   = 1000
	maxOutputLen = 30_000
)

// globInput is the JSON structure the LLM sends to invoke the tool.
type globInput struct {
	Pattern string  `json:"pattern"`
	Path    *string `json:"path,omitempty"` // directory to search in
}

// GlobTool finds files by glob patterns.
type GlobTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a GlobTool with the given config.
func New(cfg config.ToolConfig) *GlobTool {
	return &GlobTool{cfg: cfg}
}

func (t *GlobTool) Name() string                   { return toolName }
func (t *GlobTool) Description() string            { return globDescription }
func (t *GlobTool) IsReadOnly() bool               { return true }
func (t *GlobTool) IsConcurrencySafe() bool        { return true }
func (t *GlobTool) IsEnabled() bool                { return t.cfg.Enabled }

func (t *GlobTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The glob pattern to match files against",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. If not specified, the current working directory will be used.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) ValidateInput(input json.RawMessage) error {
	var gi globInput
	if err := json.Unmarshal(input, &gi); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if gi.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	return nil
}

// fileInfo holds a path and its modification time for sorting.
type fileInfo struct {
	path    string
	modTime int64 // unix timestamp
}

func (t *GlobTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var gi globInput
	if err := json.Unmarshal(input, &gi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	searchDir := "."
	if gi.Path != nil && *gi.Path != "" {
		searchDir = *gi.Path
	}

	// Use filepath.Glob for simple patterns, walk for ** patterns.
	var matches []fileInfo
	pattern := gi.Pattern

	if strings.Contains(pattern, "**") {
		// Walk-based matching for recursive patterns.
		matches = walkGlob(searchDir, pattern)
	} else {
		// Simple glob.
		fullPattern := filepath.Join(searchDir, pattern)
		paths, err := filepath.Glob(fullPattern)
		if err != nil {
			return &types.ToolResult{Content: "invalid glob pattern: " + err.Error(), IsError: true}, nil
		}
		for _, p := range paths {
			if info, err := os.Stat(p); err == nil {
				matches = append(matches, fileInfo{path: p, modTime: info.ModTime().Unix()})
			}
		}
	}

	// Sort by modification time (most recent first).
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime > matches[j].modTime
	})

	// Limit results.
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	// Format output.
	if len(matches) == 0 {
		return &types.ToolResult{
			Content: "No files matched the pattern.",
			Metadata: map[string]any{
				"render_hint": "search",
				"pattern":     gi.Pattern,
				"match_count": 0,
			},
		}, nil
	}

	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(m.path)
		sb.WriteString("\n")
	}

	output := sb.String()
	if len(output) > maxOutputLen {
		output = output[:maxOutputLen] + "\n... (output truncated)"
	}

	return &types.ToolResult{
		Content: output,
		Metadata: map[string]any{
			"render_hint": "search",
			"pattern":     gi.Pattern,
			"match_count": len(matches),
		},
	}, nil
}

// walkGlob performs recursive directory walking for ** patterns.
func walkGlob(root, pattern string) []fileInfo {
	var results []fileInfo

	// Convert ** pattern to a walkable form.
	// Split pattern into directory prefix and file pattern.
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}

		// Try to match the relative path against the pattern.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}

		matched := doubleStarMatch(pattern, rel)
		if matched {
			results = append(results, fileInfo{path: path, modTime: info.ModTime().Unix()})
		}

		return nil
	})

	return results
}

// doubleStarMatch provides basic ** glob matching.
// ** matches any number of path segments.
func doubleStarMatch(pattern, name string) bool {
	// Handle common case: **/*.ext
	if strings.HasPrefix(pattern, "**/") {
		subPattern := pattern[3:]
		// Check if the basename matches.
		baseName := filepath.Base(name)
		if matched, _ := filepath.Match(subPattern, baseName); matched {
			return true
		}
		// Also try matching the full relative path.
		parts := strings.Split(name, string(filepath.Separator))
		for i := range parts {
			remaining := filepath.Join(parts[i:]...)
			if matched, _ := filepath.Match(subPattern, remaining); matched {
				return true
			}
		}
		return false
	}

	// For other patterns, try direct match.
	matched, _ := filepath.Match(pattern, name)
	return matched
}

const globDescription = `Fast file pattern matching tool that works with any codebase size.

Usage:
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns`
