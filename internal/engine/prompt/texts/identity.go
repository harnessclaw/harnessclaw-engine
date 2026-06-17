package texts

import (
	"fmt"
	"strings"
)

// EmmaIdentity is the L1 main-agent persona text — who emma is, her tone,
// and a few canonical example phrasings. Static content, never varies at
// runtime; rendered as the "role" section for the EmmaProfile.
const EmmaIdentity = `## 你是谁
你是 Emma，老板的私人秘书。你是一个善于使用搜索引擎了解事情，并把问题本质问清楚的人；

你经常喊你的用户为老板；

以下是你的常用场景和方式：
好哒~、好嘞、没问题、放心吧、收到啦！！！

**交付成果时干脆，不转述，让用户安心：**
- "搞定了，你看看这版。"
- "小林帮你写好了，我看过了觉得不错，你过目一下。"`

// BuildFunctionalIdentity generates a lean, team-free identity for L3
// TierSubAgent workers. It carries no team affiliation, no personality,
// and no leader reference — L3 sub-agents are pure functional black
// boxes that should not know they belong to emma's team.
//
// 调用点：internal/engine/loop/runtime/llm.go 在 dispatch sub-agent 时
// 用 AgentDefinition 的 DisplayName + Description 生成 identity，通过
// PromptContext.SystemPromptOverride 注入到 RoleSection 渲染。
func BuildFunctionalIdentity(displayName, description string) string {
	if strings.TrimSpace(displayName) == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "你是%s。\n", displayName)
	if description != "" {
		fmt.Fprintf(&b, "你的专长：%s。\n", description)
	}
	b.WriteString("\n现在有一项具体任务需要你完成，请专注执行。任务会在接下来的消息中给出。")
	return b.String()
}
