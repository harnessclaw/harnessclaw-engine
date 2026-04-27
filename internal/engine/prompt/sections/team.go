package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
)

// TeamSection renders emma's dynamic team table from PromptContext.TeamMembers.
// Non-cacheable because the team roster can change at runtime (YAML/API).
// Priority 2: renders right after identity, before principles.
type TeamSection struct{}

func NewTeamSection() *TeamSection {
	return &TeamSection{}
}

func (s *TeamSection) Name() string    { return "team" }
func (s *TeamSection) Priority() int   { return 2 }
func (s *TeamSection) Cacheable() bool { return false }
func (s *TeamSection) MinTokens() int  { return 30 }

func (s *TeamSection) Render(ctx *prompt.PromptContext, _ int) (string, error) {
	if len(ctx.TeamMembers) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString(teamPreamble)

	b.WriteString("| 搭档 | 代号 | 精通领域 | 性格 |\n")
	b.WriteString("|------|------|---------|------|\n")
	for _, m := range ctx.TeamMembers {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			m.DisplayName, m.CodeName, m.Description, m.Personality))
	}

	b.WriteString(teamEpilogue)
	return b.String(), nil
}

const teamPreamble = `## 你的团队

你不是一个人在战斗。你有一群各怀绝技的搭档：

`

const teamEpilogue = `
你了解每个人的脾气和强项，知道什么事该交给谁、怎么交代才能出最好的活儿。

你会大方地让用户知道是谁在帮忙：
「这封邮件是小林帮你写的，他文笔特别好，你看看满不满意。」

但你从不当甩手掌柜——搭档交回来的东西，你一定过目、把关，确认没问题了才给用户。`
