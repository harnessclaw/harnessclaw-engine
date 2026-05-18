package searchskill

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const ToolName = "SearchSkill"

type SearchSkillTool struct {
	tool.BaseTool
	reader *skill.Reader
	logger *zap.Logger
}

func New(reader *skill.Reader, logger *zap.Logger) *SearchSkillTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SearchSkillTool{reader: reader, logger: logger}
}

func (t *SearchSkillTool) Name() string         { return ToolName }
func (t *SearchSkillTool) Description() string  { return description }
func (t *SearchSkillTool) IsReadOnly() bool     { return true }
func (t *SearchSkillTool) IsConcurrencySafe() bool { return true }

func (t *SearchSkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "可选关键词，匹配 skill 的 name / description / when_to_use（不区分大小写）。",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "返回上限，默认 20，最大 50。",
			},
		},
	}
}

type input struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type output struct {
	Skills []skill.SkillCard `json:"skills"`
	Total  int               `json:"total"`
}

func (t *SearchSkillTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	start := time.Now()
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		t.logger.Info("search skill",
			zap.String("agent_id", agentRunID),
			zap.String("outcome", "invalid_input"),
			zap.String("reason", err.Error()),
		)
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if t.reader == nil {
		t.logger.Info("search skill",
			zap.String("agent_id", agentRunID),
			zap.String("query", in.Query),
			zap.String("outcome", "reader_unset"),
		)
		return &types.ToolResult{Content: "skill reader not configured", IsError: true}, nil
	}
	cards, err := t.reader.Search(in.Query, in.Limit)
	if err != nil {
		t.logger.Info("search skill",
			zap.String("agent_id", agentRunID),
			zap.String("query", in.Query),
			zap.String("outcome", "error"),
			zap.String("reason", err.Error()),
			zap.Duration("duration", time.Since(start)),
		)
		return &types.ToolResult{Content: "search failed: " + err.Error(), IsError: true}, nil
	}
	out := output{Skills: cards, Total: len(cards)}
	body, _ := json.Marshal(out)
	names := make([]string, 0, len(cards))
	for i, c := range cards {
		if i >= 10 {
			break
		}
		names = append(names, c.Name)
	}
	outcome := "hit"
	if len(cards) == 0 {
		outcome = "miss"
	}
	t.logger.Info("search skill",
		zap.String("agent_id", agentRunID),
		zap.String("query", in.Query),
		zap.String("outcome", outcome),
		zap.Int("hit_count", len(cards)),
		zap.Strings("names", names),
		zap.Duration("duration", time.Since(start)),
	)
	return &types.ToolResult{Content: string(body)}, nil
}

const description = `搜索本地 skills 目录中可用的 skill。

返回 skill 元数据列表（name / description / when_to_use / version / allowed_tools），不含 SKILL.md body。
仅供 L2 specialists 与 L3 freelancer 使用。

调用时机：
- L2: 任务不属于固定搭档时，先调用此工具看磁盘上有哪些匹配
- L3 freelancer: 启动时给的 candidate 不合用 / 需要补充 skill 时

参数：
- query: 可选关键词
- limit: 默认 20，最大 50`
