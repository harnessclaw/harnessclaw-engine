package agent

import "harnessclaw-go/internal/tool"

const BrowserAgentName = "browser-agent"

// BrowserAgentDefinition returns the built-in leaf agent that runs browser
// observation and extraction tasks behind the browser_agent entry tool.
func BrowserAgentDefinition() *AgentDefinition {
	return &AgentDefinition{
		Name:        BrowserAgentName,
		DisplayName: "Browser Agent",
		Description: "打开网页、观察渲染结果，并提取页面信息的浏览器子 Agent",
		Tier:        TierSubAgent,
		Profile:     "worker",
		AgentType:   tool.AgentTypeSync,
		AllowedTools: []string{
			"browser_session_create",
			"browser_navigate",
			"browser_snapshot",
			"browser_extract",
			"browser_click",
			"browser_fill",
			"browser_press",
			"browser_scroll",
			"browser_screenshot",
			"browser_back",
			"browser_wait",
			"browser_tabs",
			"browser_ask_human",
			"browser_session_close",
			"web_search",
			"tavily_search",
			"web_fetch",
			"submit_task_result",
			"escalate_to_planner",
		},
		Skills:       []string{"browser", "web_extract"},
		SystemPrompt: browserAgentPrompt,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal": map[string]any{
					"type":        "string",
					"description": "浏览器任务目标。",
				},
				"start_url": map[string]any{
					"type":        "string",
					"description": "可选起始 URL。",
				},
				"max_steps": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "最多浏览器操作步数。",
				},
			},
			"required": []string{"goal"},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"content", "source"},
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "最终提取或整理给主 Agent 的内容。",
				},
				"source": map[string]any{
					"type":        "string",
					"enum":        []string{"direct_access", "search_fallback", "api_fallback", "partial"},
					"description": "内容来源：直接访问、浏览器搜索降级、搜索 API 降级或部分结果。",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "可选说明，例如登录墙、验证码、超时或信息不完整原因。",
				},
			},
		},
		Limitations: []string{
			"只执行浏览器信息采集和基础降级，不处理文件编辑或代码修改。",
			"遇到登录墙、验证码或站点限制时必须明确说明结果来源和不确定性。",
		},
		ExampleTasks: []string{
			"打开目标网站并提取渲染后的榜单内容。",
			"访问一个 SPA 页面，读取可见文本并整理摘要。",
		},
		CostTier:         CostMedium,
		TypicalLatencyMs: 30000,
	}
}

const browserAgentPrompt = `你是 Browser Agent，负责在独立浏览器会话中完成网页信息采集。

工作流程：
1. 先调用 browser_session_create 创建浏览器会话，读取返回的 cdp_endpoint。
2. 对目标 URL 调用 browser_navigate。
3. 优先用 browser_snapshot 观察可访问性树；页面变化后 ref 会失效，必须重新 snapshot。
4. 需要交互时使用 browser_click / browser_fill / browser_press / browser_scroll / browser_wait / browser_back / browser_tabs。
5. 需要页面全文时调用 browser_extract；AX Tree 不足以判断时再用 browser_screenshot。
6. 遇到登录、验证码或站点确认时使用 browser_ask_human 请求用户接管。
7. 直接访问失败时，先在同一浏览器会话内访问搜索引擎兜底；浏览器整体不可用时再用 web_search / tavily_search / web_fetch。
8. 结束前尽量调用 browser_session_close 清理会话。

要求：
- 不要编造页面内容；只能基于工具结果作答。
- 使用搜索或 API 降级时，在结果里标注 source=search_fallback 或 source=api_fallback。
- 最终必须调用 submit_task_result，result 至少包含 content 和 source。`
