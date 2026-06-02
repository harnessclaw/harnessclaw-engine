package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
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

func withCDP(endpoint string, command ...string) []string {
	endpoint = strings.TrimSpace(endpoint)
	args := []string{"--session", browserAgentSessionName(endpoint), "--cdp", endpoint, "--json"}
	args = append(args, command...)
	return args
}

func browserAgentSessionName(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "harnessclaw-browser-" + hex.EncodeToString(sum[:])[:16]
}

func BrowserTaskSessionName(taskID string) string {
	return "harnessclaw-browser-" + sanitizeSessionName(taskID)
}

func CleanupHelperSession(ctx context.Context, cfg config.BrowserAgentConfig, runner Runner, taskID string) (*types.ToolResult, error) {
	sessionName := BrowserTaskSessionName(taskID)
	args := withHelperSession(cfg, sessionName, "stream", "disable")
	return runAgentBrowser(ctx, cfg, withRunner(cfg, runner), args)
}

func withHelperSession(_ config.BrowserAgentConfig, sessionName string, command ...string) []string {
	args := []string{"--session", sessionName, "--json"}
	args = append(args, command...)
	return args
}

func validateCDPEndpoint(endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("cdp_endpoint is required")
	}
	if strings.HasPrefix(endpoint, "ws://") || strings.HasPrefix(endpoint, "wss://") {
		return nil
	}
	return fmt.Errorf("cdp_endpoint must be a ws:// or wss:// CDP URL")
}

func cdpEndpointSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "可选的本地 CDP WebSocket endpoint；当前 Browser Agent 已绑定 endpoint 时由 HarnessClaw 自动注入。",
		"minLength":   1,
	}
}
