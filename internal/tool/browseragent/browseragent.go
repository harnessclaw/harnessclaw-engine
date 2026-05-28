package browseragent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const ToolName = "browser_agent"

type Tool struct {
	tool.BaseTool
	spawner agent.AgentSpawner
	cfg     config.BrowserAgentConfig
	logger  *zap.Logger
}

type input struct {
	Goal     string `json:"goal"`
	StartURL string `json:"start_url,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

func New(spawner agent.AgentSpawner, cfg config.BrowserAgentConfig, logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{spawner: spawner, cfg: cfg, logger: logger.Named("browseragent")}
}

func (t *Tool) Name() string                  { return ToolName }
func (t *Tool) Description() string           { return description }
func (t *Tool) IsReadOnly() bool              { return false }
func (t *Tool) IsConcurrencySafe() bool       { return true }
func (t *Tool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *Tool) IsLongRunning() bool           { return true }
func (t *Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyDangerous }

func (t *Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "浏览器任务目标，例如“打开小红书并提取今日热榜”。",
				"minLength":   1,
			},
			"start_url": map[string]any{
				"type":        "string",
				"description": "可选起始 URL；没有时由 browser-agent 自行决定入口。",
				"format":      "uri",
			},
			"max_steps": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "最大浏览器操作步数，默认取配置 max_steps。",
			},
		},
		"required": []string{"goal"},
	}
}

func (t *Tool) ValidateInput(raw json.RawMessage) error {
	in, err := parseInput(raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	maxSteps := t.maxSteps()
	if in.MaxSteps < 0 {
		return fmt.Errorf("max_steps must be positive")
	}
	if in.MaxSteps > maxSteps {
		return fmt.Errorf("max_steps must be <= %d", maxSteps)
	}
	if strings.TrimSpace(in.StartURL) != "" {
		if err := validateStartURL(in.StartURL, t.cfg.BlockedDomains); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	in, err := parseInput(raw)
	if err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if t.spawner == nil {
		return &types.ToolResult{Content: "browser_agent has no AgentSpawner", IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	maxSteps := in.MaxSteps
	if maxSteps == 0 {
		maxSteps = t.maxSteps()
	}
	taskID := "browser_" + uuid.New().String()[:8]
	cfg := &agent.SpawnConfig{
		Prompt:       buildPrompt(in, maxSteps, t.cfg),
		AgentType:    tool.AgentTypeSync,
		SubagentType: agent.BrowserAgentName,
		Name:         agent.BrowserAgentName,
		Description:  "浏览器任务",
		MaxTurns:     maxSteps,
		Timeout:      5 * time.Minute,
		TaskID:       taskID,
		Inputs: map[string]any{
			"goal":      strings.TrimSpace(in.Goal),
			"max_steps": maxSteps,
		},
	}
	if strings.TrimSpace(in.StartURL) != "" {
		cfg.Inputs["start_url"] = strings.TrimSpace(in.StartURL)
	}
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		cfg.ParentSessionID = tuc.Core.SessionID
	}
	if rootSID, ok := sessionstats.RootSessionIDFromCtx(ctx); ok {
		cfg.RootSessionID = rootSID
	}
	if out, ok := tool.GetEventOut(ctx); ok {
		cfg.ParentOut = out
	}

	t.logger.Info("spawning browser sub-agent", zap.Int("max_steps", maxSteps), zap.Bool("has_start_url", in.StartURL != ""))
	result, err := t.spawner.SpawnSync(ctx, cfg)
	if err != nil {
		return &types.ToolResult{Content: "Browser Agent execution failed: " + err.Error(), IsError: true, ErrorType: types.ToolErrorDependencyFail}, nil
	}
	meta := map[string]any{
		"render_hint":   "agent",
		"session_id":    result.SessionID,
		"agent_id":      result.AgentID,
		"num_turns":     result.NumTurns,
		"subagent_type": agent.BrowserAgentName,
	}
	if result.Terminal != nil {
		meta["terminal_reason"] = string(result.Terminal.Reason)
	}
	return &types.ToolResult{
		Content:  result.Output,
		IsError:  agent.IsTerminalError(result),
		Metadata: meta,
	}, nil
}

func parseInput(raw json.RawMessage) (input, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("invalid browser_agent input: %w", err)
	}
	return in, nil
}

func (t *Tool) maxSteps() int {
	if t.cfg.MaxSteps > 0 {
		return t.cfg.MaxSteps
	}
	return 30
}

func buildPrompt(in input, maxSteps int, cfg config.BrowserAgentConfig) string {
	var b strings.Builder
	b.WriteString("浏览器任务目标：")
	b.WriteString(strings.TrimSpace(in.Goal))
	b.WriteString("\n\n")
	if strings.TrimSpace(in.StartURL) != "" {
		b.WriteString("起始 URL：")
		b.WriteString(strings.TrimSpace(in.StartURL))
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("最大操作步数：%d\n", maxSteps))
	if cfg.PreferredSearchEngine != "" {
		b.WriteString("搜索降级优先使用：")
		b.WriteString(cfg.PreferredSearchEngine)
		b.WriteString("\n")
	}
	visibility := strings.TrimSpace(cfg.DefaultVisibility)
	if visibility != "hidden" && visibility != "visible" {
		visibility = "visible"
	}
	b.WriteString("浏览器窗口可见性：")
	b.WriteString(visibility)
	b.WriteString("\n")
	b.WriteString("\n执行要求：先调用 browser_session_create 创建浏览器会话")
	b.WriteString(fmt.Sprintf("，并传入 visibility=%q", visibility))
	if visibility == "visible" {
		b.WriteString("，让浏览器窗口显示在台前，方便用户观察进度")
	}
	if strings.TrimSpace(in.StartURL) != "" {
		b.WriteString("；创建完成后必须调用 browser_navigate 访问起始 URL，不要依赖 browser_session_create 加载 start_url")
	} else {
		b.WriteString("；需要打开网页时必须调用 browser_navigate")
	}
	b.WriteString("；然后使用返回的 cdp_endpoint 调用浏览器操作；直接访问失败时按浏览器搜索、搜索 API、WebFetch 顺序降级；最后调用 submit_task_result，用 result 返回 content 和 source。")
	return b.String()
}

func validateStartURL(raw string, blocked []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("start_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("start_url scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("start_url host is required")
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range blocked {
		d := strings.TrimSpace(strings.ToLower(domain))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return fmt.Errorf("start_url domain is blocked")
		}
	}
	return nil
}

const description = `可以使用真实浏览器：当用户询问是否能使用浏览器时，应回答可以通过 browser_agent 工具启动 browser-agent 子 Agent。

browser_agent 会启动专用 browser-agent 子 Agent 来完成网页信息采集任务，适合需要真实浏览器渲染、SPA 页面读取、目标网站直接访问和搜索降级的任务。`
