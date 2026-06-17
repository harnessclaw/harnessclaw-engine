package builtin

import (
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
)

// PlanDesign 是 Plan Mode 的执行体，对应 dispatch subagent_type="plan"。
//
// 定位：用户提出复杂任务时，调度方派 plan 来做**只读**任务拆解 —— 不立即
// 执行，把拆解结果写到 task_dir 内的 plan.md 文件里供用户审阅。
//
// 严格约束（principles + AllowedTools 双重保险）：
//   - 只允许只读工具（read / glob / grep / web_*）
//   - 唯一可写的对象：自己 task_dir 里的 plan.md
//   - 禁用 bash / dispatch / freelance —— 不能跑命令、不能派别人
//
// 跟 freelancer / content_creator 的区别：那些是"动手做"，plan 是"先想清楚
// 怎么做，写下来等用户拍板"。
var PlanDesign = definition.AgentDefinition{
	Name:         "plan",
	DisplayName:  "规划者",
	Description:  "用户提出复杂任务时对任务进行拆解，把分步步骤写到 plan.md",
	AgentType:    tool.AgentTypeSync,
	Profile:      "plan",
	IsTeamMember: true,
	IsBuiltin:    true,
	// 200 容下复杂项目的多轮 read/grep/web_search 调研。plan 是只读探查 +
	// 写 plan.md，单次任务可能要扫几十个文件 + 多次搜索 + 多次 edit 增量
	// 更新 plan.md，15 turn 不够。
	MaxTurns: 200,
	AllowedTools: []string{
		// 只读探查
		"read",
		"glob",
		"grep",
		// 唯一允许的写入：仅限 task_dir/plan.md（principles 约束 + LLM 自律）
		"write",
		"edit",
		// 网络调研
		"web_search",
		"tavily_search",
		"web_fetch",
		// 终止合约
		"meta_write",
		"submit_task_result",
	},
}
