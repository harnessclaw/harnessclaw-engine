package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	NavigateToolName = "browser_navigate"
	SnapshotToolName = "browser_snapshot"
	ExtractToolName  = "browser_extract"
)

type NavigateTool struct {
	tool.BaseTool
	cfg    config.BrowserAgentConfig
	runner Runner
}

type navigateInput struct {
	CDPEndpoint string `json:"cdp_endpoint"`
	URL         string `json:"url"`
}

func NewNavigateTool(cfg config.BrowserAgentConfig, runner Runner) *NavigateTool {
	return &NavigateTool{cfg: cfg, runner: withRunner(cfg, runner)}
}

func (t *NavigateTool) Name() string { return NavigateToolName }
func (t *NavigateTool) Description() string {
	return "通过 agent-browser 打开 URL。需要传入 browser_session_create 返回的 cdp_endpoint。"
}
func (t *NavigateTool) IsReadOnly() bool              { return false }
func (t *NavigateTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *NavigateTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }

func (t *NavigateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cdp_endpoint": cdpEndpointSchema(),
			"url": map[string]any{
				"type":        "string",
				"description": "要打开的 http/https URL。",
				"format":      "uri",
			},
		},
		"required": []string{"cdp_endpoint", "url"},
	}
}

func (t *NavigateTool) ValidateInput(raw json.RawMessage) error {
	var in navigateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_navigate input: %w", err)
	}
	if err := validateCDPEndpoint(in.CDPEndpoint); err != nil {
		return err
	}
	if err := validateHTTPURL(in.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if domainBlocked(in.URL, t.cfg.BlockedDomains) {
		return fmt.Errorf("url domain is blocked")
	}
	return nil
}

func (t *NavigateTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	var in navigateInput
	_ = json.Unmarshal(raw, &in)
	args := withCDP(in.CDPEndpoint, "open", in.URL)
	return runAgentBrowser(ctx, t.cfg, t.runner, args)
}

type SnapshotTool struct {
	tool.BaseTool
	cfg    config.BrowserAgentConfig
	runner Runner
}

type snapshotInput struct {
	CDPEndpoint string `json:"cdp_endpoint"`
	Interactive *bool  `json:"interactive,omitempty"`
	Compact     bool   `json:"compact,omitempty"`
	IncludeURLs bool   `json:"include_urls,omitempty"`
	Depth       int    `json:"depth,omitempty"`
	Selector    string `json:"selector,omitempty"`
}

func NewSnapshotTool(cfg config.BrowserAgentConfig, runner Runner) *SnapshotTool {
	return &SnapshotTool{cfg: cfg, runner: withRunner(cfg, runner)}
}

func (t *SnapshotTool) Name() string { return SnapshotToolName }
func (t *SnapshotTool) Description() string {
	return "读取当前页面的 accessibility tree 快照。默认只返回可交互元素，ref 在页面变化后会失效。"
}
func (t *SnapshotTool) IsReadOnly() bool              { return true }
func (t *SnapshotTool) IsConcurrencySafe() bool       { return false }
func (t *SnapshotTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *SnapshotTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }

func (t *SnapshotTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cdp_endpoint": cdpEndpointSchema(),
			"interactive": map[string]any{
				"type":        "boolean",
				"description": "是否只返回可交互元素；默认 true。",
			},
			"compact": map[string]any{
				"type":        "boolean",
				"description": "是否移除空结构节点。",
			},
			"include_urls": map[string]any{
				"type":        "boolean",
				"description": "链接元素是否带 href URL。",
			},
			"depth": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "限制树深度。",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "可选 CSS selector，用于限制快照范围。",
			},
		},
		"required": []string{"cdp_endpoint"},
	}
}

func (t *SnapshotTool) ValidateInput(raw json.RawMessage) error {
	var in snapshotInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_snapshot input: %w", err)
	}
	if err := validateCDPEndpoint(in.CDPEndpoint); err != nil {
		return err
	}
	if in.Depth < 0 {
		return fmt.Errorf("depth must be positive")
	}
	return nil
}

func (t *SnapshotTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	var in snapshotInput
	_ = json.Unmarshal(raw, &in)
	cmd := []string{"snapshot"}
	if in.Interactive == nil || *in.Interactive {
		cmd = append(cmd, "-i")
	}
	if in.Compact {
		cmd = append(cmd, "-c")
	}
	if in.IncludeURLs {
		cmd = append(cmd, "-u")
	}
	if in.Depth > 0 {
		cmd = append(cmd, "-d", fmt.Sprintf("%d", in.Depth))
	}
	if strings.TrimSpace(in.Selector) != "" {
		cmd = append(cmd, "-s", in.Selector)
	}
	return runAgentBrowser(ctx, t.cfg, t.runner, withCDP(in.CDPEndpoint, cmd...))
}

type ExtractTool struct {
	tool.BaseTool
	cfg    config.BrowserAgentConfig
	runner Runner
}

type extractInput struct {
	CDPEndpoint string `json:"cdp_endpoint"`
	Selector    string `json:"selector,omitempty"`
	Format      string `json:"format,omitempty"`
}

func NewExtractTool(cfg config.BrowserAgentConfig, runner Runner) *ExtractTool {
	return &ExtractTool{cfg: cfg, runner: withRunner(cfg, runner)}
}

func (t *ExtractTool) Name() string { return ExtractToolName }
func (t *ExtractTool) Description() string {
	return "提取当前页面内容。默认读取 body 的可见文本；可选择 text/html/title/url。"
}
func (t *ExtractTool) IsReadOnly() bool              { return true }
func (t *ExtractTool) IsConcurrencySafe() bool       { return false }
func (t *ExtractTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *ExtractTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }

func (t *ExtractTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cdp_endpoint": cdpEndpointSchema(),
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector 或 @ref。format 为 text/html 时默认 body。",
			},
			"format": map[string]any{
				"type":        "string",
				"description": "提取格式。",
				"enum":        []string{"text", "html", "title", "url"},
			},
		},
		"required": []string{"cdp_endpoint"},
	}
}

func (t *ExtractTool) ValidateInput(raw json.RawMessage) error {
	var in extractInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_extract input: %w", err)
	}
	if err := validateCDPEndpoint(in.CDPEndpoint); err != nil {
		return err
	}
	switch in.Format {
	case "", "text", "html", "title", "url":
	default:
		return fmt.Errorf("format must be one of text, html, title, url")
	}
	return nil
}

func (t *ExtractTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	var in extractInput
	_ = json.Unmarshal(raw, &in)
	format := in.Format
	if format == "" {
		format = "text"
	}
	cmd := []string{"get", format}
	if format == "text" || format == "html" {
		selector := strings.TrimSpace(in.Selector)
		if selector == "" {
			selector = "body"
		}
		cmd = append(cmd, selector)
	}
	return runAgentBrowser(ctx, t.cfg, t.runner, withCDP(in.CDPEndpoint, cmd...))
}

func NewTools(cfg config.BrowserAgentConfig) []tool.Tool {
	runner := NewCommandRunner(cfg)
	return []tool.Tool{
		NewSessionCreateTool(cfg),
		NewSessionCloseTool(cfg),
		NewNavigateTool(cfg, runner),
		NewSnapshotTool(cfg, runner),
		NewExtractTool(cfg, runner),
		NewClickTool(cfg, runner),
		NewFillTool(cfg, runner),
		NewPressTool(cfg, runner),
		NewScrollTool(cfg, runner),
		NewScreenshotTool(cfg, runner),
		NewBackTool(cfg, runner),
		NewWaitTool(cfg, runner),
		NewTabsTool(cfg, runner),
		NewAskHumanTool(cfg),
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
		"description": "browser_session_create 返回的本地 CDP WebSocket endpoint。",
		"minLength":   1,
	}
}
