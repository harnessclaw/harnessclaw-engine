package sections

import (
	"harnessclaw-go/internal/legacy/prompt"
)

// SkillsSection renders the available skills listing into the system prompt.
// This replaces the per-turn <system-reminder> user message injection (Layer 3),
// making skill listing part of the structured system prompt with proper
// token budget allocation and API-level prompt caching.
//
// The intro text adapts to the caller's available tools:
//   - search_skill present (L2 scheduler) → guidance about freelancer dispatch
//   - Skill present (L1 emma) → guidance about direct Skill tool invocation
//   - Neither (e.g. a worker that somehow received a listing) → minimal text
type SkillsSection struct{}

func NewSkillsSection() *SkillsSection {
	return &SkillsSection{}
}

func (s *SkillsSection) Name() string    { return "skills" }
func (s *SkillsSection) Priority() int   { return 35 } // after tools(20), before task(40)
func (s *SkillsSection) Cacheable() bool { return true }
func (s *SkillsSection) MinTokens() int  { return 30 }

func (s *SkillsSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if ctx.SkillListing == "" {
		return "", nil
	}

	hasSearch := false
	hasDirect := false
	for _, t := range ctx.AvailableTools {
		switch t.Name() {
		case "search_skill":
			hasSearch = true
		case "skill":
			hasDirect = true
		}
	}

	var intro string
	switch {
	case hasSearch:
		// L2 scheduler: skills are routed through freelancer L3.
		intro = "# 可用技能（user-installed skills）\n\n" +
			"以下 skill 是用户在本地 skills 目录下装载的。**任务匹配某 skill 时，请优先派 freelancer 处理**：\n\n" +
			"1. 用 search_skill(query=\"...\") 验证当前状态（skill 可能已删除/新增）\n" +
			"2. 调用 task(subagent_type=\"freelancer\", candidate_skills=[匹配的 skill 名]) 派活\n\n" +
			"判断规则：\n" +
			"- 任务里出现明确文件格式名（docx/pdf/xlsx/notion 等）→ 几乎必有对应 skill，先 search_skill\n" +
			"- 任务匹配下方 skill description → 派 freelancer，**不要走 developer 跑通用脚本**\n\n"
	case hasDirect:
		// L1 emma path: direct skill tool invocation (Claude Code style).
		intro = "# 可用技能\n\n" +
			"使用 skill 工具调用以下技能。" +
			"当用户请求匹配某项技能时，先调用技能再生成其他回复。\n\n"
	default:
		// Fallback: agent received the listing but has no obvious entry tool.
		// Keep it minimal — pure informational.
		intro = "# 可用技能（参考）\n\n以下 skill 在系统中可用：\n\n"
	}

	return intro + ctx.SkillListing, nil
}
