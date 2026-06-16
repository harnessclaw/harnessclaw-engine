package browser_agent

import (
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
)

// BrowserAgentDefinition returns the built-in leaf agent that runs browser
// observation and extraction tasks behind the browser_agent entry tool.
func BrowserAgentDefinition() *definition.AgentDefinition {
	return &definition.AgentDefinition{
		Name:        AgentName,
		DisplayName: "Browser Agent",
		Description: "打开网页、观察渲染结果，并提取页面信息的浏览器子 Agent",
		Tier:        definition.TierSubAgent,
		Profile:     "worker",
		AgentType:   tool.AgentTypeSync,
		AllowedTools: []string{
			"browser_session_create",
			"browser_session_state",
			"browser_session_close",
			"browser_ask_human",
			"agent_browser_command",
			"browser_skill_reference",
			"browser_agent_final_result",
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
					"enum":        []string{"browser", "partial"},
					"description": "内容来源：浏览器直接操作结果，或明确标注的不完整浏览器结果。",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "可选说明，例如登录墙、验证码、超时或信息不完整原因。",
				},
				"evidence": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "可选证据摘要，例如 URL、标题或关键页面事实。",
				},
			},
		},
		Limitations: []string{
			"只执行浏览器信息采集，不处理文件编辑、代码修改或非浏览器降级路径。",
			"遇到登录墙、验证码或站点限制时必须明确说明结果来源和不确定性。",
		},
		ExampleTasks: []string{
			"打开目标网站并提取渲染后的榜单内容。",
			"访问一个 SPA 页面，读取可见文本并整理摘要。",
		},
		CostTier:         definition.CostMedium,
		TypicalLatencyMs: 30000,
	}
}

const browserAgentPrompt = `你是 Browser Agent，负责在独立浏览器会话中完成网页信息采集。

任务边界：
- 本 Browser Agent 只处理一个目标站点或一个浏览器会话。
- 如果任务包含多个互相独立的站点、账号、窗口或浏览器会话，不要在当前 Agent 内串联处理；返回 partial 并说明应由主 Agent 拆成多个 browser_agent 调用分别执行。

工作流程：
1. 先调用 browser_session_create 创建并绑定当前 Browser Agent 的浏览器会话；返回内容不包含 CDP endpoint，HarnessClaw 会在私有 binding 中维护 endpoint 和 CLI session，不要读取、传入或复用任何 endpoint；浏览器使用客户端全局持久 profile，登录态会跨聊天会话、跨浏览器 session、关闭窗口后继续复用，不要为普通任务传 task_id 或 partition 创建隔离 profile。
2. 使用 agent_browser_command 执行浏览器操作（打开页面、快照、点击、填写、截图等），具体操作语义参考已加载的 official agent-browser skill；SKILL.md 信息不足时，用 browser_skill_reference 按需读取对应 reference。
3. 遇到登录、验证码、扫码、MFA 或站点确认时，必须使用 browser_ask_human 请求用户接管；用户完成后调用 browser_session_state 刷新并重新绑定当前活动标签页，再继续操作，不要因为这类卡点直接结束任务。
4. 不使用搜索、API 抓取或其他非浏览器降级路径；目标页面无法通过浏览器完成时，返回明确的 partial 结果和原因。
5. 普通 turn 完成后不要主动关闭浏览器；客户端会自动隐藏窗口并保留 session。只有用户明确要求关闭、窗口不可恢复或需要释放资源时才调用 browser_session_close。

要求：
- 不要编造页面内容；只能基于工具结果作答。
- result.source 只能使用 browser 或 partial。
- 登录、验证码、扫码等人类操作完成后，继续当前浏览器会话；不要主动关闭或重开浏览器。
- 显式关闭浏览器只关闭窗口/session 句柄，不清理全局持久 profile；下次打开仍应复用已有登录态。
- 最终必须调用 browser_agent_final_result，至少包含 content 和 source；不要直接调用 submit_task_result。`
