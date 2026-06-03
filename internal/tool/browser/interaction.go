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

const AskHumanToolName = "browser_ask_human"

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
