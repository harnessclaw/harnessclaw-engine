package browser_agent

// AgentName 是子代理身份名（AgentDefinition.Name / SubagentType）。
// 这是用户/调度层看见的稳定标识。
const AgentName = "browser-agent"

// ToolName 是 LLM 发起 browser-agent 任务时使用的 tool_use 名（带下划线
// 因为很多 LLM provider 对工具名字符集有限制）。
//
// 集中在本"内核"文件里，让 tools/builtin/browseragent（调用工具）
// 与 engine/agent/builtin/browser_agent（执行体）都依赖它，避免两包
// 互相 import 形成循环。
const ToolName = "browser_agent"
