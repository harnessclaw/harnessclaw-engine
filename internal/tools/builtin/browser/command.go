package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const AgentBrowserCommandToolName = "agent_browser_command"

// harnessOwnedFlags are flags injected by the harness; the model must not supply them.
var harnessOwnedFlags = []string{"--cdp", "--session", "--json"}

type AgentBrowserCommandTool struct {
	tool.BaseTool
	cfg    config.BrowserAgentConfig
	runner Runner
}

type commandInput struct {
	Args []string `json:"args"`
}

func NewAgentBrowserCommandTool(cfg config.BrowserAgentConfig, runner Runner) *AgentBrowserCommandTool {
	return &AgentBrowserCommandTool{cfg: cfg, runner: withRunner(cfg, runner)}
}

func (t *AgentBrowserCommandTool) Name() string { return AgentBrowserCommandToolName }
func (t *AgentBrowserCommandTool) Description() string {
	return "通过 agent-browser CLI 执行浏览器命令。先创建并绑定当前 Browser Agent 的浏览器会话；HarnessClaw 会私下注入 --cdp、--session、--json 标志，模型不要读取、传入或复用 endpoint。"
}
func (t *AgentBrowserCommandTool) IsReadOnly() bool              { return false }
func (t *AgentBrowserCommandTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *AgentBrowserCommandTool) IsLongRunning() bool           { return true }
func (t *AgentBrowserCommandTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyDangerous }

func (t *AgentBrowserCommandTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
				"description": "agent-browser argv，第一项是子命令，如 snapshot/open/click。不得包含 --cdp、--session、--json（由 harness 注入）。",
			},
		},
		"required": []string{"args"},
	}
}

func (t *AgentBrowserCommandTool) ValidateInput(raw json.RawMessage) error {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("invalid agent_browser_command input: %w", err)
	}
	for _, forbidden := range []string{"cdp_endpoint", "session_id"} {
		if _, ok := payload[forbidden]; ok {
			return fmt.Errorf("%s is managed by HarnessClaw private browser binding and must not be supplied", forbidden)
		}
	}

	var in commandInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid agent_browser_command input: %w", err)
	}
	if len(in.Args) == 0 {
		return fmt.Errorf("args is required")
	}
	for _, arg := range in.Args {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("args must not contain blank argv items")
		}
		for _, flag := range harnessOwnedFlags {
			if arg == flag {
				return fmt.Errorf("arg %q is reserved by the harness and must not be supplied by the model", arg)
			}
		}
	}
	return nil
}

func (t *AgentBrowserCommandTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	var in commandInput
	_ = json.Unmarshal(raw, &in)

	sessionName, endpoint, err := commandTargetFromContext(ctx)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	args := withCommandGatewayCDP(t.cfg, sessionName, endpoint, in.Args...)

	result, err := runAgentBrowser(ctx, t.cfg, t.runner, args)
	if err != nil {
		return result, err
	}

	// Apply output cap
	if t.cfg.MaxOutputBytes > 0 && len(result.Content) > t.cfg.MaxOutputBytes {
		result.Content = result.Content[:t.cfg.MaxOutputBytes]
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.Metadata["truncated"] = true
		result.Metadata["max_output_bytes"] = t.cfg.MaxOutputBytes
	}

	return result, nil
}

func withCommandGatewayCDP(cfg config.BrowserAgentConfig, sessionName, endpoint string, command ...string) []string {
	args := []string{"--session", sessionName, "--cdp", strings.TrimSpace(endpoint), "--json"}
	if cfg.ContentBoundaries {
		args = append(args, "--content-boundaries")
	}
	if cfg.MaxOutputBytes > 0 {
		args = append(args, "--max-output", fmt.Sprintf("%d", cfg.MaxOutputBytes))
	}
	if len(cfg.AllowedDomains) > 0 {
		args = append(args, "--allowed-domains", strings.Join(cfg.AllowedDomains, ","))
	}
	if strings.TrimSpace(cfg.ActionPolicyPath) != "" {
		args = append(args, "--action-policy", strings.TrimSpace(cfg.ActionPolicyPath))
	}
	if len(cfg.ConfirmActions) > 0 {
		args = append(args, "--confirm-actions", strings.Join(cfg.ConfirmActions, ","))
	}
	args = append(args, command...)
	return args
}

func commandTargetFromContext(ctx context.Context) (string, string, error) {
	if binding, ok := taskBindingFromContext(ctx); ok {
		if binding.IsReady() {
			return binding.SessionName(), binding.CDPEndpoint(), nil
		}
	}
	return "", "", fmt.Errorf("请先创建浏览器会话：调用 browser_session_create 后再使用 agent_browser_command")
}
