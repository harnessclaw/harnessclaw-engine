package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const (
	SessionCreateToolName = "browser_session_create"
	SessionCloseToolName  = "browser_session_close"
	SessionStateToolName  = "browser_session_state"
)

type SessionCreateTool struct {
	tool.BaseTool
	cfg config.BrowserAgentConfig
}

type sessionCreateInput struct {
	StartURL   string `json:"start_url,omitempty"`
	Visibility string `json:"visibility,omitempty"`
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
				"description": "可选起始 URL，仅用于任务记录和校验；创建会话后应使用 agent_browser_command 调用 official agent-browser open/goto 命令打开该地址。",
				"format":      "uri",
			},
			"visibility": map[string]any{
				"type":        "string",
				"description": "窗口默认可见性。",
				"enum":        []string{"hidden", "visible"},
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

type SessionStateTool struct {
	tool.BaseTool
	cfg config.BrowserAgentConfig
}

type sessionStateInput struct {
	SessionID string `json:"session_id"`
	WindowID  string `json:"window_id,omitempty"`
}

func NewSessionStateTool(cfg config.BrowserAgentConfig) *SessionStateTool {
	return &SessionStateTool{cfg: cfg}
}

func (t *SessionStateTool) Name() string         { return SessionStateToolName }
func (t *SessionStateTool) Description() string  { return sessionStateDescription }
func (t *SessionStateTool) IsReadOnly() bool     { return true }
func (t *SessionStateTool) IsEnabled() bool      { return t.cfg.Enabled }
func (t *SessionStateTool) IsClientRouted() bool { return true }
func (t *SessionStateTool) SafetyLevel() tool.SafetyLevel {
	return tool.SafetySafe
}

func (t *SessionStateTool) InputSchema() map[string]any {
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

func (t *SessionStateTool) ValidateInput(raw json.RawMessage) error {
	var in sessionStateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_session_state input: %w", err)
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	return nil
}

func (t *SessionStateTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	return &types.ToolResult{
		Content:   "browser_session_state is a client-routed tool; it must be executed by the connected Electron client.",
		IsError:   true,
		ErrorType: types.ToolErrorDependencyFail,
	}, nil
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

const sessionCreateDescription = `创建并绑定当前 Browser Agent 的独立浏览器会话窗口，由 Electron 客户端执行。

浏览器使用客户端全局持久 profile；登录态、cookies、localStorage 和 IndexedDB 会跨聊天会话、跨浏览器 session、关闭窗口后继续复用。

返回值只包含 session_id、window_id、visible、active_tab、tabs、closed/human_takeover 等可见状态；CDP endpoint 由 HarnessClaw 私有绑定到当前 Browser Agent，模型不要读取、传入或复用 endpoint。`

const sessionCloseDescription = `关闭 browser_session_create 创建的浏览器会话，由 Electron 客户端执行。

普通任务完成后不要主动调用该工具；客户端会在 turn 结束时隐藏窗口并保留 session。用户显式关闭也只销毁窗口/session 句柄，不清理全局持久 profile 或登录态。只有用户明确要求关闭、窗口不可恢复或需要释放资源时才调用。`

const sessionStateDescription = `读取浏览器会话当前状态，由 Electron 客户端执行。

返回值包含 visible、active_tab、tabs 等可见状态，不包含 CDP endpoint。用户完成登录、验证码、扫码或手动切换标签页后，应调用该工具刷新并重新绑定当前 Browser Agent 的活动标签页，再继续操作。`
