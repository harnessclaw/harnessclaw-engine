package builtin

import (
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
)

// ContentCreator 是内容创作专家 agent，对应 dispatch subagent_type="content_creator"。
// 负责 AI 图片与视频生成：image_generate 生图、video_create 起视频任务（异步），
// video_query 轮询拿结果。20 turns 预留给视频生成的多轮 poll。
var ContentCreator = definition.AgentDefinition{
	Name:         "content_creator",
	DisplayName:  "创作家(Luna)",
	Description:  "内容创作专家，精通 AI 图片与视频制作。image_generate 生图；video_create 起视频任务（异步），再用 video_query 轮询拿结果",
	AgentType:    tool.AgentTypeSync,
	Profile:      "content_creator",
	IsTeamMember: true,
	IsBuiltin:    true,
	Personality:  "审美在线，对画面节奏与画风一致性敏感",
	// 200 容下视频生成的长轮询（video_query 多轮 poll）+ 后期合成步骤。
	// 比之前 20 大一档：video_create 后异步等几分钟 + 多次 query 才能拿到
	// video_path，过去经常因 turn 用光而失败。
	MaxTurns: 200,
	AllowedTools: []string{
		// 核心：AI 图 / 视频生成
		"image_generate",
		"video_create",
		"video_query",
		// 通用文件操作 —— 找 / 读 / 改 / 写素材文件与配套文档
		// （read/edit/write 走 PermissionManager 门控，跟 freelancer 同档）
		"read",
		"edit",
		"write",
		"glob",
		"grep",
		// shell —— 用来跑 ffmpeg / 批量重命名 / 调外部工具做后期合成等
		"bash",
		// 找参考资料
		"web_search",
		"tavily_search",
		"web_fetch",
		// 任务终止
		"meta_write",
		"submit_task_result",
	},
}
