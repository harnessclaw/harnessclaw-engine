package browseragent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const ToolName = "browser_agent"

type Tool struct {
	tool.BaseTool
	sched  scheduler.Scheduler
	cfg    config.BrowserAgentConfig
	logger *zap.Logger
}

type input struct {
	Goal     string `json:"goal"`
	StartURL string `json:"start_url,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

func New(sched scheduler.Scheduler, cfg config.BrowserAgentConfig, logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{sched: sched, cfg: cfg, logger: logger.Named("browseragent")}
}

func (t *Tool) Name() string                  { return ToolName }
func (t *Tool) Description() string           { return description }
func (t *Tool) IsReadOnly() bool              { return false }
func (t *Tool) IsConcurrencySafe() bool       { return false }
func (t *Tool) IsEnabled() bool               { return t.cfg.Enabled }
func (t *Tool) IsLongRunning() bool           { return true }
func (t *Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyDangerous }

func (t *Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "单个目标站点或单个浏览器会话的任务目标，例如“打开小红书并提取今日热榜”。如果用户需求包含多个独立站点、账号、窗口或浏览器会话，应拆成多个 browser_agent 调用。",
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
	if t.sched == nil {
		return &types.ToolResult{Content: "browser_agent has no scheduler", IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	maxSteps := in.MaxSteps
	if maxSteps == 0 {
		maxSteps = t.maxSteps()
	}

	// 用内置 BrowserAgentDefinition；新 scheduler 通过 SpawnParams.Definition 驱动 Runtime.LLM。
	def := agent.BrowserAgentDefinition()

	inputs := map[string]any{
		"goal":      strings.TrimSpace(in.Goal),
		"max_steps": maxSteps,
	}
	if strings.TrimSpace(in.StartURL) != "" {
		inputs["start_url"] = strings.TrimSpace(in.StartURL)
	}

	// 父身份完整三元组：SessionID + AgentID + StepID(tool_use_id)
	parentRef := &scheduler.ParentRef{}
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		parentRef.SessionID = types.SessionID(tuc.Core.SessionID)
		parentRef.StepID = tuc.Core.ToolCallID
	}
	if sid, ok := sessionstats.SessionIDFromCtx(ctx); ok && sid != "" {
		parentRef.AgentID = types.AgentID(sid)
	}
	if rootSID, ok := sessionstats.RootSessionIDFromCtx(ctx); ok && rootSID != "" {
		parentRef.RootSessionID = types.SessionID(rootSID)
	}
	var events chan<- types.EngineEvent
	if out, ok := tool.GetEventOut(ctx); ok {
		events = out
	}

	t.logger.Info("spawning browser sub-agent", zap.Int("max_steps", maxSteps), zap.Bool("has_start_url", in.StartURL != ""))

	res, err := t.sched.Dispatch(ctx, scheduler.SpawnParams{
		Definition:  *def,
		Prompt:      buildPrompt(in, maxSteps, t.cfg),
		Name:        agent.BrowserAgentName,
		Description: "浏览器任务",
		Parent:      parentRef,
		InvokedBy:   scheduler.Invoker{Kind: scheduler.InvokerLLM, Source: ToolName},
		Inputs:      inputs,
		Events:      events,
		Overrides:   scheduler.Overrides{MaxTurns: maxSteps, Timeout: 5 * time.Minute},
	})
	if err != nil {
		return &types.ToolResult{Content: "Browser Agent execution failed: " + err.Error(), IsError: true, ErrorType: types.ToolErrorDependencyFail}, nil
	}

	sync, ok := res.Outcome.(scheduler.SyncOutcome)
	if !ok {
		return &types.ToolResult{Content: "Browser Agent: expected SyncOutcome", IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	meta := map[string]any{
		"render_hint":   "agent",
		"agent_id":      res.AgentID,
		"task_id":       res.TaskID,
		"subagent_type": agent.BrowserAgentName,
		"tool_calls":    sync.ToolCalls,
	}
	if sync.Terminal.Reason != "" {
		meta["terminal_reason"] = string(sync.Terminal.Reason)
	}

	isError := sync.Terminal.Reason != "" && sync.Terminal.Reason != types.TerminalCompleted
	return &types.ToolResult{
		Content:  concatText(sync.Content),
		IsError:  isError,
		Metadata: meta,
	}, nil
}

// concatText 把 ContentBlock 列表的 text 字段拼接成单段输出。
func concatText(blocks []types.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == types.ContentTypeText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func parentAgentIDFromContext(ctx context.Context, tuc *types.ToolUseContext) string {
	if producer, ok := tool.GetArtifactProducer(ctx); ok {
		if agentID := strings.TrimSpace(producer.AgentID); agentID != "" {
			return agentID
		}
	}
	if tuc != nil {
		if agentID := strings.TrimSpace(tuc.Agent.AgentID); agentID != "" {
			return agentID
		}
	}
	return "main"
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
	visibility := strings.TrimSpace(cfg.DefaultVisibility)
	if visibility != "hidden" && visibility != "visible" {
		visibility = "hidden"
	}
	b.WriteString("浏览器窗口可见性：")
	b.WriteString(visibility)
	b.WriteString("\n")
	b.WriteString("\n任务边界：本 Browser Agent 只处理一个目标站点或一个浏览器会话；如果原始需求包含多个互相独立的站点、账号、窗口或浏览器会话，应由主 Agent 拆成多个 browser_agent 调用分别执行，不要在同一个 Browser Agent 内串联多个独立浏览器目标。\n")
	b.WriteString(fmt.Sprintf("\n执行要求：先调用 browser_session_create 创建浏览器会话，并传入 visibility=%q", visibility))
	if visibility == "visible" {
		b.WriteString("，让浏览器窗口显示在台前，方便用户观察进度")
	}
	if strings.TrimSpace(in.StartURL) != "" {
		b.WriteString("；创建完成后使用 agent_browser_command 打开起始 URL")
	} else {
		b.WriteString("；需要打开网页时使用 agent_browser_command")
	}
	b.WriteString("；浏览器使用客户端全局持久 profile，登录态、cookies、localStorage 和 IndexedDB 会跨聊天会话、跨浏览器 session、关闭窗口后继续复用，不要传 task_id 或 partition 创建隔离 profile；HarnessClaw 会把 browser_session_create 或 browser_session_state 返回的最新 cdp_endpoint 绑定到当前 Browser Agent，调用 agent_browser_command 时不要复用其他 Browser Agent 的 endpoint；遇到登录、验证码、扫码、MFA 或站点确认时调用 browser_ask_human，让用户操作后调用 browser_session_state 取回当前 active_tab.cdp_endpoint 再继续；不使用非浏览器降级路径，目标页面无法通过浏览器完成时返回 partial 和原因；最后调用 browser_agent_final_result，用 result 返回 content 和 source；普通 turn 完成后不要主动关闭浏览器，客户端会自动隐藏窗口；显式关闭也只关闭窗口/session 句柄，不清理登录态。")
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

browser_agent 会启动专用 browser-agent 子 Agent 来完成网页信息采集任务，适合需要真实浏览器渲染、SPA 页面读取、目标网站直接访问和搜索降级的任务。

每次 browser_agent 调用只处理一个目标站点或一个浏览器会话；如果用户需求包含多个互相独立的站点、账号、窗口或浏览器会话，主 Agent 应拆成多个 browser_agent 调用分别执行并汇总结果。`
