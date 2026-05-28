package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	SessionCreateToolName = "browser_session_create"
	SessionCloseToolName  = "browser_session_close"
)

type SessionCreateTool struct {
	tool.BaseTool
	cfg config.BrowserAgentConfig
}

type sessionCreateInput struct {
	StartURL   string `json:"start_url,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Partition  string `json:"partition,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
}

func NewSessionCreateTool(cfg config.BrowserAgentConfig) *SessionCreateTool {
	return &SessionCreateTool{cfg: cfg}
}

func (t *SessionCreateTool) Name() string         { return SessionCreateToolName }
func (t *SessionCreateTool) Description() string  { return sessionCreateDescription }
func (t *SessionCreateTool) IsReadOnly() bool     { return false }
func (t *SessionCreateTool) IsEnabled() bool      { return t.cfg.Enabled }
func (t *SessionCreateTool) IsClientRouted() bool { return true }
func (t *SessionCreateTool) SafetyLevel() tool.SafetyLevel {
	return tool.SafetyDangerous
}

func (t *SessionCreateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"start_url": map[string]any{
				"type":        "string",
				"description": "可选起始 URL，仅用于任务记录和校验；创建会话后应使用 browser_navigate 打开该地址。",
				"format":      "uri",
			},
			"visibility": map[string]any{
				"type":        "string",
				"description": "窗口默认可见性。",
				"enum":        []string{"hidden", "visible"},
			},
			"partition": map[string]any{
				"type":        "string",
				"description": "Electron session partition，可省略由客户端按策略生成。",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "浏览器任务 ID，用于隔离一次性 partition。",
			},
		},
	}
}

func (t *SessionCreateTool) ValidateInput(raw json.RawMessage) error {
	var in sessionCreateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_session_create input: %w", err)
	}
	if in.Visibility != "" && in.Visibility != "hidden" && in.Visibility != "visible" {
		return fmt.Errorf("visibility must be hidden or visible")
	}
	if in.StartURL != "" {
		if err := validateHTTPURL(in.StartURL); err != nil {
			return fmt.Errorf("start_url: %w", err)
		}
		if domainBlocked(in.StartURL, t.cfg.BlockedDomains) {
			return fmt.Errorf("start_url domain is blocked")
		}
	}
	return nil
}

func (t *SessionCreateTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	return &types.ToolResult{
		Content:   "browser_session_create is a client-routed tool; it must be executed by the connected Electron client.",
		IsError:   true,
		ErrorType: types.ToolErrorDependencyFail,
	}, nil
}

type SessionCloseTool struct {
	tool.BaseTool
	cfg config.BrowserAgentConfig
}

type sessionCloseInput struct {
	SessionID string `json:"session_id"`
	WindowID  string `json:"window_id,omitempty"`
}

func NewSessionCloseTool(cfg config.BrowserAgentConfig) *SessionCloseTool {
	return &SessionCloseTool{cfg: cfg}
}

func (t *SessionCloseTool) Name() string         { return SessionCloseToolName }
func (t *SessionCloseTool) Description() string  { return sessionCloseDescription }
func (t *SessionCloseTool) IsReadOnly() bool     { return false }
func (t *SessionCloseTool) IsEnabled() bool      { return t.cfg.Enabled }
func (t *SessionCloseTool) IsClientRouted() bool { return true }
func (t *SessionCloseTool) SafetyLevel() tool.SafetyLevel {
	return tool.SafetyCaution
}

func (t *SessionCloseTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "browser_session_create 返回的 session_id。",
				"minLength":   1,
			},
			"window_id": map[string]any{
				"type":        "string",
				"description": "可选 BrowserWindow ID。",
			},
		},
		"required": []string{"session_id"},
	}
}

func (t *SessionCloseTool) ValidateInput(raw json.RawMessage) error {
	var in sessionCloseInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_session_close input: %w", err)
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	return nil
}

func (t *SessionCloseTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	return &types.ToolResult{
		Content:   "browser_session_close is a client-routed tool; it must be executed by the connected Electron client.",
		IsError:   true,
		ErrorType: types.ToolErrorDependencyFail,
	}, nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("URL host is required")
	}
	return nil
}

func domainBlocked(raw string, blocked []string) bool {
	if len(blocked) == 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		h, _, err := net.SplitHostPort(u.Host)
		if err == nil {
			host = strings.ToLower(h)
		}
	}
	for _, domain := range blocked {
		d := strings.TrimSpace(strings.ToLower(domain))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

const sessionCreateDescription = `创建独立的浏览器会话窗口，由 Electron 客户端执行。

返回值应包含 session_id、window_id 和 cdp_endpoint。拿到 cdp_endpoint 后，后续 browser_navigate / browser_snapshot / browser_extract 调用必须把它作为 cdp_endpoint 参数传入。`

const sessionCloseDescription = `关闭 browser_session_create 创建的浏览器会话，由 Electron 客户端执行。完成浏览器任务后应尽量调用该工具释放窗口资源。`
