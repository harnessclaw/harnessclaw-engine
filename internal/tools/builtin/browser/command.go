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
	SessionID   string   `json:"session_id,omitempty"`
	CDPEndpoint string   `json:"cdp_endpoint,omitempty"`
	Args        []string `json:"args"`
}

func NewAgentBrowserCommandTool(cfg config.BrowserAgentConfig, runner Runner) *AgentBrowserCommandTool {
	return &AgentBrowserCommandTool{cfg: cfg, runner: withRunner(cfg, runner)}
}

func (t *AgentBrowserCommandTool) Name() string { return AgentBrowserCommandToolName }
func (t *AgentBrowserCommandTool) Description() string {
	return "通过 agent-browser CLI 执行浏览器命令。官方 skill 适配器，由 harness 注入 --cdp、--session、--json 标志。"
}
func (t *AgentBrowserCommandTool) IsReadOnly() bool              { return false }
func (t *AgentBrowserCommandTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *AgentBrowserCommandTool) IsLongRunning() bool           { return true }
func (t *AgentBrowserCommandTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyDangerous }

func (t *AgentBrowserCommandTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cdp_endpoint": cdpEndpointSchema(),
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
	var in commandInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid agent_browser_command input: %w", err)
	}
	if strings.TrimSpace(in.CDPEndpoint) != "" {
		if err := validateCDPEndpoint(in.CDPEndpoint); err != nil {
			return err
		}
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

	sessionName, endpoint, err := commandTargetFromContext(ctx, in.SessionID, in.CDPEndpoint)
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

func commandTargetFromContext(ctx context.Context, modelSessionID, endpoint string) (string, string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if binding, ok := taskBindingFromContext(ctx); ok {
		sessionName := binding.SessionName()
		if supplied := strings.TrimSpace(modelSessionID); supplied != "" && supplied != sessionName {
			return "", "", fmt.Errorf("session_id %q does not match current Browser Agent session %q", supplied, sessionName)
		}
		if expected := binding.CDPEndpoint(); expected != "" {
			if endpoint != "" && endpoint != expected {
				return "", "", fmt.Errorf("cdp_endpoint %q does not match current Browser Agent endpoint %q", endpoint, expected)
			}
			return sessionName, expected, nil
		}
		if err := validateCDPEndpoint(endpoint); err != nil {
			return "", "", err
		}
		return sessionName, endpoint, nil
	}
	if taskID := taskIDFromContext(ctx); taskID != "" {
		want := BrowserTaskSessionName(taskID)
		if supplied := strings.TrimSpace(modelSessionID); supplied != "" && supplied != want {
			return "", "", fmt.Errorf("session_id %q does not match current Browser Agent session %q", supplied, want)
		}
		if err := validateCDPEndpoint(endpoint); err != nil {
			return "", "", err
		}
		return want, endpoint, nil
	}
	if s := strings.TrimSpace(modelSessionID); s != "" {
		if err := validateCDPEndpoint(endpoint); err != nil {
			return "", "", err
		}
		return BrowserTaskSessionName(s), endpoint, nil
	}
	if err := validateCDPEndpoint(endpoint); err != nil {
		return "", "", err
	}
	return browserAgentSessionName(endpoint), endpoint, nil
}

func sanitizeSessionName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}
