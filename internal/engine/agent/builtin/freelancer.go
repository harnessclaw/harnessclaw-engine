package builtin

import (
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
)

// Freelancer 是 user-skill-driven L3 sub-agent。能力由 spawn 时装载的
// skill 决定 —— freelancer module shim 会把 SKILL.md body 注入 prompt。
// 走 FreelancerProfile，strip dispatch tools（strict L3 leaf）。
//
// AllowedTools 是项目静态声明的，skill 不能扩。Bash / FileWrite 之类
// 的高危工具仍走 PermissionManager 门控，跟 skill 内容无关。
var Freelancer = definition.AgentDefinition{
	Name:         "freelancer",
	DisplayName:  "多面手",
	Description:  "通用执行体，能力由装载的 user skill 决定（来自本地 skills/ 目录）",
	Tier:         definition.TierSubAgent,
	Profile:      "freelancer",
	AgentType:    tool.AgentTypeSync,
	IsTeamMember: true,
	IsBuiltin:    true,
	// 200 容下复杂任务的多文件编辑 / 多步调研 / skill 反复 load+unload。
	// 之前没设、loop runtime 兜底 30 turn，实际不少长任务需要更多。
	MaxTurns: 200,
	AllowedTools: []string{
		// 通用文件 / shell，受 PermissionManager 门控。
		// Tool registry key 用的是 Tool.Name() 的字面值 ——
		// "read" / "edit" / "write"，不是 "FileRead" / "FileEdit" / "FileWrite"。
		"read", "edit", "write", "glob", "bash",
		// Web + artifact
		"web_fetch", "web_search", "tavily_search",
		"meta_write",
		// Skill 自管理
		"search_skill", "load_skill", "unload_skill", "list_loaded_skills",
		// Terminal tools（MaybeAugmentForSubAgent 会自动加，这里列出来便于阅读）。
		// "做不到"用 meta_write({status:"failed"}) + submit_task_result 表达，
		// 不再有 escalate_to_planner。
		"submit_task_result",
	},
	// InputSchema 校验结构化的 cfg.Inputs。实际任务描述走 cfg.Prompt，
	// 不进 cfg.Inputs —— 所以这里 properties 只声明 candidate_skills。
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"candidate_skills": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"maxItems":    3,
				"description": "L2 预选的 skill 名字列表。spawn 时框架会预加载它们的 SKILL.md body 到 freelancer 上下文。",
			},
		},
	},
	OutputSchema: map[string]any{
		"type":     "object",
		"required": []string{"artifact_role", "skills_used"},
		"properties": map[string]any{
			"artifact_role": map[string]any{
				"type":        "string",
				"description": "本次产出的角色标签（由 skill 上下文决定具体语义）。",
			},
			"skills_used": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "实际影响本次产出的 skill 名字（不一定等于所有加载的 skill）。",
			},
		},
	},
	CostTier: definition.CostMedium,
	Limitations: []string{
		"能力完全取决于装载的 skill",
		"上下文中并存 skill body 数量上限 3（含 L2 预分配）",
		"skill 行为偏离不应硬走，应 EscalateToPlanner",
	},
}
