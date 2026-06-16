package browser

import (
	"context"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

func NewTools(cfg config.BrowserAgentConfig) []tool.Tool {
	runner := NewCommandRunner(cfg)
	return []tool.Tool{
		NewSessionCreateTool(cfg),
		NewSessionStateTool(cfg),
		NewSessionCloseTool(cfg),
		NewAskHumanTool(cfg),
		NewAgentBrowserCommandTool(cfg, runner),
		NewSkillReferenceTool(cfg),
		NewFinalResultTool(),
	}
}

func NewPhase1Tools(cfg config.BrowserAgentConfig) []tool.Tool {
	return NewTools(cfg)
}

func withRunner(cfg config.BrowserAgentConfig, runner Runner) Runner {
	if runner != nil {
		return runner
	}
	return NewCommandRunner(cfg)
}

func CleanupBoundHelperSession(ctx context.Context, cfg config.BrowserAgentConfig, runner Runner, binding *TaskBinding) (*types.ToolResult, error) {
	if binding == nil || strings.TrimSpace(binding.SessionName()) == "" {
		return &types.ToolResult{Content: "browser helper session cleanup skipped; no bound browser session"}, nil
	}
	args := withHelperSession(cfg, binding.SessionName(), "stream", "disable")
	return runAgentBrowser(ctx, cfg, withRunner(cfg, runner), args)
}

func withHelperSession(_ config.BrowserAgentConfig, sessionName string, command ...string) []string {
	args := []string{"--session", sessionName, "--json"}
	args = append(args, command...)
	return args
}
