// Package bash implements the Bash tool for executing shell commands.
package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	toolName = "Bash"

	// maxOutputLen is the maximum number of characters returned to the LLM.
	maxOutputLen = 30_000

	// maxTimeout caps the per-call timeout regardless of input.
	maxTimeout = 10 * time.Minute

	// defaultTimeout is used when neither config nor input specifies a timeout.
	defaultTimeout = 2 * time.Minute
)

// bashInput is the JSON structure the LLM sends to invoke the tool.
type bashInput struct {
	Command      string  `json:"command"`
	Timeout      *int    `json:"timeout,omitempty"`      // milliseconds
	Description  *string `json:"description,omitempty"`
	RunInBG      *bool   `json:"run_in_background,omitempty"`
}

// BashTool executes shell commands and returns their output.
type BashTool struct {
	tool.BaseTool
	cfg       config.ToolConfig
	shell     string     // resolved shell binary
	cwd       string     // current working directory (persists across calls)
	cwdMu     sync.Mutex
	originCwd string     // initial CWD for reset
}

// New creates a BashTool configured from the given ToolConfig.
func New(cfg config.ToolConfig) *BashTool {
	shell := resolveShell()
	cwd, _ := os.Getwd()

	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}

	return &BashTool{
		cfg:       cfg,
		shell:     shell,
		cwd:       cwd,
		originCwd: cwd,
	}
}

func (b *BashTool) Name() string        { return toolName }
func (b *BashTool) Description() string  { return getDescription() }
func (b *BashTool) IsReadOnly() bool             { return false }
func (b *BashTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyDangerous }
func (b *BashTool) IsEnabled() bool      { return b.cfg.Enabled }

// InputSchema returns the JSON Schema describing accepted parameters.
func (b *BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "要执行的 bash 命令。",
			},
			"timeout": map[string]any{
				"type":        "number",
				"description": "可选超时时间（毫秒，最长 600000）。",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "用一句话清楚说明这条命令在做什么。",
			},
			"run_in_background": map[string]any{
				"type":        "boolean",
				"description": "为 true 时在后台运行该命令。",
			},
		},
		"required": []string{"command"},
	}
}

// ValidateInput checks that the command field is present and non-empty.
func (b *BashTool) ValidateInput(input json.RawMessage) error {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return fmt.Errorf("command must not be empty")
	}
	return nil
}

// Execute runs the shell command and returns its combined stdout+stderr output.
func (b *BashTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &types.ToolResult{
			Content: fmt.Sprintf("invalid input: %v", err),
			IsError: true,
		}, nil
	}

	// Background commands are not yet supported.
	if in.RunInBG != nil && *in.RunInBG {
		return &types.ToolResult{
			Content: "background execution is not yet supported",
			IsError: true,
		}, nil
	}

	// Resolve timeout: input (ms) > config > default. Cap at maxTimeout.
	timeout := b.cfg.Timeout
	if in.Timeout != nil && *in.Timeout > 0 {
		timeout = time.Duration(*in.Timeout) * time.Millisecond
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	// Create a child context with the tool-level timeout.
	// The parent ctx may already have the engine-level timeout; this is the inner cap.
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Snapshot and later update CWD.
	b.cwdMu.Lock()
	cwd := b.cwd
	b.cwdMu.Unlock()

	// If CWD no longer exists on disk, reset to origin.
	if _, err := os.Stat(cwd); err != nil {
		cwd = b.originCwd
		b.cwdMu.Lock()
		b.cwd = cwd
		b.cwdMu.Unlock()
	}

	// Create a temp file for CWD tracking.
	cwdFile, err := os.CreateTemp("", "bash-cwd-*")
	if err != nil {
		return &types.ToolResult{
			Content: fmt.Sprintf("failed to create temp file: %v", err),
			IsError: true,
		}, nil
	}
	cwdFilePath := cwdFile.Name()
	cwdFile.Close()
	defer os.Remove(cwdFilePath)

	// Wrap the command to track CWD changes.
	// Run the user command in the current shell (no subshell) so cd takes effect.
	// Capture exit code, then write the resulting pwd to a temp file.
	wrapped := fmt.Sprintf(
		`cd %s && %s
__ec=$?; pwd -P > %s; exit $__ec`,
		shellQuote(cwd), in.Command, shellQuote(cwdFilePath),
	)

	start := time.Now()
	cmd := exec.CommandContext(execCtx, b.shell, "-c", wrapped)
	cmd.Dir = cwd
	cmd.Env = buildEnv()

	// Platform-specific process group setup for clean timeout killing.
	setProcAttr(cmd)

	output, execErr := cmd.CombinedOutput()
	duration := time.Since(start)

	// Update CWD from the temp file.
	if newCwd, err := os.ReadFile(cwdFilePath); err == nil {
		trimmed := strings.TrimSpace(string(newCwd))
		if trimmed != "" {
			if _, statErr := os.Stat(trimmed); statErr == nil {
				b.cwdMu.Lock()
				b.cwd = trimmed
				b.cwdMu.Unlock()
			}
		}
	}

	// Determine exit code.
	exitCode := 0
	if execErr != nil {
		var exitErr *exec.ExitError
		if errors.As(execErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	// Check if the context caused the failure (timeout or cancellation).
	isCtxErr := execCtx.Err() != nil

	// Format output.
	content := strings.TrimRight(string(output), " \t\n\r")
	if isCtxErr {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			content += "\n\n[command timed out]"
		} else {
			content += "\n\n[command cancelled]"
		}
	} else if exitCode != 0 {
		content += fmt.Sprintf("\n\n[exit code: %d]", exitCode)
	}

	content = truncateOutput(content, maxOutputLen)

	return &types.ToolResult{
		Content: content,
		IsError: isCtxErr,
		Metadata: map[string]any{
			"render_hint": "terminal",
			"exit_code":   exitCode,
			"duration_ms": duration.Milliseconds(),
			"command":     in.Command,
		},
	}, nil
}

// Cwd returns the tool's current working directory. Safe for concurrent use.
func (b *BashTool) Cwd() string {
	b.cwdMu.Lock()
	defer b.cwdMu.Unlock()
	return b.cwd
}

// buildEnv returns the environment for child processes.
func buildEnv() []string {
	env := os.Environ()
	env = append(env, "GIT_EDITOR=true", "CLAUDECODE=1")
	return env
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// truncateOutput limits output to maxLen characters.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	truncated := s[:maxLen]
	remaining := s[maxLen:]
	lines := strings.Count(remaining, "\n")
	if !strings.HasSuffix(remaining, "\n") {
		lines++
	}
	return truncated + fmt.Sprintf("\n\n... [%d lines truncated] ...", lines)
}
