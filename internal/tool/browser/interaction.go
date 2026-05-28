package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	ClickToolName      = "browser_click"
	FillToolName       = "browser_fill"
	PressToolName      = "browser_press"
	ScrollToolName     = "browser_scroll"
	ScreenshotToolName = "browser_screenshot"
	BackToolName       = "browser_back"
	WaitToolName       = "browser_wait"
	TabsToolName       = "browser_tabs"
	AskHumanToolName   = "browser_ask_human"
)

type actionTool struct {
	tool.BaseTool
	cfg         config.BrowserAgentConfig
	runner      Runner
	name        string
	description string
	build       func(json.RawMessage) (string, []string, error)
}

func (t *actionTool) Name() string                  { return t.name }
func (t *actionTool) Description() string           { return t.description }
func (t *actionTool) IsReadOnly() bool              { return false }
func (t *actionTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *actionTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (t *actionTool) InputSchema() map[string]any   { return actionSchema(t.name) }

func (t *actionTool) ValidateInput(raw json.RawMessage) error {
	cdp, _, err := t.build(raw)
	if err != nil {
		return err
	}
	return validateCDPEndpoint(cdp)
}

func (t *actionTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	cdp, cmd, err := t.build(raw)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if err := validateCDPEndpoint(cdp); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	return runAgentBrowser(ctx, t.cfg, t.runner, withCDP(cdp, cmd...))
}

func NewClickTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        ClickToolName,
		description: "点击 browser_snapshot 返回的 @ref 元素。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Ref         string `json:"ref"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_click input: %w", err)
			}
			if strings.TrimSpace(in.Ref) == "" {
				return in.CDPEndpoint, nil, fmt.Errorf("ref is required")
			}
			return in.CDPEndpoint, []string{"click", strings.TrimSpace(in.Ref)}, nil
		},
	}
}

func NewFillTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        FillToolName,
		description: "清空并填写 browser_snapshot 返回的 @ref 输入框。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Ref         string `json:"ref"`
				Text        string `json:"text"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_fill input: %w", err)
			}
			if strings.TrimSpace(in.Ref) == "" {
				return in.CDPEndpoint, nil, fmt.Errorf("ref is required")
			}
			return in.CDPEndpoint, []string{"fill", strings.TrimSpace(in.Ref), in.Text}, nil
		},
	}
}

func NewPressTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        PressToolName,
		description: "发送键盘按键，如 Enter、Tab、Escape。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Key         string `json:"key"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_press input: %w", err)
			}
			if strings.TrimSpace(in.Key) == "" {
				return in.CDPEndpoint, nil, fmt.Errorf("key is required")
			}
			return in.CDPEndpoint, []string{"press", strings.TrimSpace(in.Key)}, nil
		},
	}
}

func NewScrollTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        ScrollToolName,
		description: "按方向滚动页面或元素。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Direction   string `json:"direction"`
				Amount      int    `json:"amount,omitempty"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_scroll input: %w", err)
			}
			direction := strings.TrimSpace(in.Direction)
			if direction == "" {
				direction = "down"
			}
			switch direction {
			case "up", "down", "left", "right":
			default:
				return in.CDPEndpoint, nil, fmt.Errorf("direction must be up, down, left, or right")
			}
			if in.Amount <= 0 {
				in.Amount = 600
			}
			return in.CDPEndpoint, []string{"scroll", direction, fmt.Sprintf("%d", in.Amount)}, nil
		},
	}
}

func NewBackTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        BackToolName,
		description: "返回浏览器历史上一页。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_back input: %w", err)
			}
			return in.CDPEndpoint, []string{"back"}, nil
		},
	}
}

func NewWaitTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        WaitToolName,
		description: "等待页面 load state、selector 或 @ref 出现。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				LoadState   string `json:"load_state,omitempty"`
				Selector    string `json:"selector,omitempty"`
				Ref         string `json:"ref,omitempty"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_wait input: %w", err)
			}
			switch {
			case strings.TrimSpace(in.LoadState) != "":
				return in.CDPEndpoint, []string{"wait", "--load", strings.TrimSpace(in.LoadState)}, nil
			case strings.TrimSpace(in.Ref) != "":
				return in.CDPEndpoint, []string{"wait", strings.TrimSpace(in.Ref)}, nil
			case strings.TrimSpace(in.Selector) != "":
				return in.CDPEndpoint, []string{"wait", strings.TrimSpace(in.Selector)}, nil
			default:
				return in.CDPEndpoint, nil, fmt.Errorf("one of load_state, ref, or selector is required")
			}
		},
	}
}

func NewTabsTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        TabsToolName,
		description: "管理浏览器标签页：list/switch/close。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Action      string `json:"action"`
				Index       int    `json:"index,omitempty"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_tabs input: %w", err)
			}
			action := strings.TrimSpace(in.Action)
			if action == "" || action == "list" {
				return in.CDPEndpoint, []string{"tab"}, nil
			}
			switch action {
			case "switch":
				return in.CDPEndpoint, []string{"tab", fmt.Sprintf("%d", in.Index)}, nil
			case "close":
				return in.CDPEndpoint, []string{"tab", "close", fmt.Sprintf("%d", in.Index)}, nil
			default:
				return in.CDPEndpoint, nil, fmt.Errorf("action must be list, switch, or close")
			}
		},
	}
}

func NewScreenshotTool(cfg config.BrowserAgentConfig, runner Runner) tool.Tool {
	return &actionTool{
		cfg:         cfg,
		runner:      withRunner(cfg, runner),
		name:        ScreenshotToolName,
		description: "截取当前页面截图，可选 ref 标注。",
		build: func(raw json.RawMessage) (string, []string, error) {
			var in struct {
				CDPEndpoint string `json:"cdp_endpoint"`
				Path        string `json:"path"`
				Annotate    bool   `json:"annotate,omitempty"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", nil, fmt.Errorf("invalid browser_screenshot input: %w", err)
			}
			if strings.TrimSpace(in.Path) == "" {
				return in.CDPEndpoint, nil, fmt.Errorf("path is required")
			}
			cmd := []string{"screenshot"}
			if in.Annotate {
				cmd = append(cmd, "--annotate")
			}
			cmd = append(cmd, in.Path)
			return in.CDPEndpoint, cmd, nil
		},
	}
}

type AskHumanTool struct {
	tool.BaseTool
	cfg config.BrowserAgentConfig
}

type askHumanInput struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	WindowID  string `json:"window_id,omitempty"`
}

func NewAskHumanTool(cfg config.BrowserAgentConfig) *AskHumanTool {
	return &AskHumanTool{cfg: cfg}
}

func (t *AskHumanTool) Name() string { return AskHumanToolName }
func (t *AskHumanTool) Description() string {
	return "请求用户接管浏览器窗口，例如完成登录、验证码或站点确认。"
}
func (t *AskHumanTool) IsReadOnly() bool              { return false }
func (t *AskHumanTool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *AskHumanTool) IsClientRouted() bool          { return true }
func (t *AskHumanTool) IsLongRunning() bool           { return true }
func (t *AskHumanTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }

func (t *AskHumanTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{"type": "string", "minLength": 1},
			"message":    map[string]any{"type": "string", "minLength": 1},
			"window_id":  map[string]any{"type": "string"},
		},
		"required": []string{"session_id", "message"},
	}
}

func (t *AskHumanTool) ValidateInput(raw json.RawMessage) error {
	var in askHumanInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_ask_human input: %w", err)
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(in.Message) == "" {
		return fmt.Errorf("message is required")
	}
	return nil
}

func (t *AskHumanTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	return &types.ToolResult{
		Content:   "browser_ask_human is a client-routed tool; it must be executed by the connected Electron client.",
		IsError:   true,
		ErrorType: types.ToolErrorDependencyFail,
	}, nil
}

func actionSchema(name string) map[string]any {
	props := map[string]any{
		"cdp_endpoint": cdpEndpointSchema(),
	}
	required := []string{"cdp_endpoint"}
	switch name {
	case ClickToolName:
		props["ref"] = map[string]any{"type": "string", "minLength": 1}
		required = append(required, "ref")
	case FillToolName:
		props["ref"] = map[string]any{"type": "string", "minLength": 1}
		props["text"] = map[string]any{"type": "string"}
		required = append(required, "ref", "text")
	case PressToolName:
		props["key"] = map[string]any{"type": "string", "minLength": 1}
		required = append(required, "key")
	case ScrollToolName:
		props["direction"] = map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}}
		props["amount"] = map[string]any{"type": "integer", "minimum": 1}
	case WaitToolName:
		props["load_state"] = map[string]any{"type": "string"}
		props["selector"] = map[string]any{"type": "string"}
		props["ref"] = map[string]any{"type": "string"}
	case TabsToolName:
		props["action"] = map[string]any{"type": "string", "enum": []string{"list", "switch", "close"}}
		props["index"] = map[string]any{"type": "integer", "minimum": 0}
	case ScreenshotToolName:
		props["path"] = map[string]any{"type": "string", "minLength": 1}
		props["annotate"] = map[string]any{"type": "boolean"}
		required = append(required, "path")
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}
