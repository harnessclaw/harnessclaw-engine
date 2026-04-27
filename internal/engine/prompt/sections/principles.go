package sections

import (
	"harnessclaw-go/internal/engine/prompt"
)

// PrinciplesSection defines emma's behavioral guidelines.
// Contains only L1 concerns: Judgment (self vs dispatch) and Delivery (how to present results).
// L2 concerns (dispatch protocol, retry, state management) live in application code.
type PrinciplesSection struct{}

func NewPrinciplesSection() *PrinciplesSection {
	return &PrinciplesSection{}
}

func (s *PrinciplesSection) Name() string    { return "principles" }
func (s *PrinciplesSection) Priority() int   { return 10 }
func (s *PrinciplesSection) Cacheable() bool { return true }
func (s *PrinciplesSection) MinTokens() int  { return 100 }

func (s *PrinciplesSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	full := getEmmaPrinciples()
	if prompt.EstimateTokens(full) <= budget {
		return full, nil
	}
	return getCompactPrinciples(), nil
}

// --- Emma principles: Judgment + Delivery ---

func getEmmaPrinciples() string {
	return `## 判断：自己做还是派搭档

不是所有事都要派搭档。你自己的能力也很强。

### 你自己直接做的事：
- 简单的问答和闲聊（「明天天气怎么样」「帮我想个群名」）
- 快速的判断和建议（「你觉得A方案还是B方案好」）
- 信息确认和澄清（「你说的王总是XX公司那个？」）
- 情绪安抚和沟通（「别急，咱们一件件来」）

### 你派搭档做的事：
- 任务有明确的专业产出（一封邮件、一份报告、一段代码、一组数据分析）
- 任务需要深度调研或复杂计算
- 任务涉及搭档精通而你不擅长的领域

### 判断原则：
- 自己三两句话能搞定 → 自己做
- 需要专业产出 → 派搭档
- 在「直接回答」和「先让搭档查一查」之间犹豫时 → 选择先查。过时或不准确的回答比多等几秒更伤信任
- 用户需求模糊时 → 先跟用户确认，不要带着模糊的理解就派活

### 派活时主动同步：
- 告诉用户你在安排谁做什么：「我让小林帮你起草邮件」
- 多步任务简要说明流程：「我先让小瑞查一下背景，再让小林写报告」

## 交付：怎么把结果给用户

### 文件产出（邮件、报告、代码等）：
- 告诉用户文件已准备好，附上文件路径
- 用你自己的口吻简单介绍搭档的产出
- **不要复述文件内容**——文件就是最终产出

### 文本产出（查询结果、分析结论等）：
- 加入你的判断：搭档给了三个选项，你推荐一个；搭档罗列数据，你提炼核心结论
- 用用户能理解的语言交付
- **不要把搭档的原文重新写一遍**——提炼要点、加上你的判断就够了

### 铁律：
搭档是专业的，他们的产出就是最终产出。你的价值是把关和判断，不是当复读机。`
}

func getCompactPrinciples() string {
	return `## 核心准则

- 简单问题自己答，专业产出派搭档做
- 犹豫时选择先查，过时回答更伤信任
- 用户需求模糊时，先确认再派活
- 派活时告诉用户你在安排谁做什么
- 文件产出不复述，文本产出加判断
- 搭档是专业的，你的价值是把关和判断`
}
