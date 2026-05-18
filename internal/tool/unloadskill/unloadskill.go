package unloadskill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const ToolName = "UnloadSkill"

type UnloadSkillTool struct {
	tool.BaseTool
	logger *zap.Logger
}

func New(logger *zap.Logger) *UnloadSkillTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnloadSkillTool{logger: logger}
}

func (t *UnloadSkillTool) Name() string         { return ToolName }
func (t *UnloadSkillTool) Description() string  { return description }
func (t *UnloadSkillTool) IsReadOnly() bool     { return false }

func (t *UnloadSkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"skill"},
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "要卸载的 skill 名字。",
			},
		},
	}
}

type input struct {
	Skill string `json:"skill"`
}

func (t *UnloadSkillTool) ValidateInput(raw json.RawMessage) error {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if in.Skill == "" {
		return fmt.Errorf("skill is required")
	}
	return nil
}

func (t *UnloadSkillTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		t.logger.Info("unload skill",
			zap.String("agent_id", agentRunID),
			zap.String("outcome", "invalid_input"),
			zap.String("reason", err.Error()),
		)
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	trackerVal, ok := tool.GetSkillTrackerValue(ctx)
	if !ok {
		t.logger.Info("unload skill",
			zap.String("agent_id", agentRunID),
			zap.String("skill", in.Skill),
			zap.String("outcome", "tracker_missing"),
			zap.String("reason", "non-freelancer caller"),
		)
		return &types.ToolResult{
			Content: "UnloadSkill is only available to freelancer sub-agents",
			IsError: true,
		}, nil
	}
	tracker, ok := trackerVal.(*engine.SkillTracker)
	if !ok {
		t.logger.Info("unload skill",
			zap.String("agent_id", agentRunID),
			zap.String("skill", in.Skill),
			zap.String("outcome", "tracker_type_error"),
		)
		return &types.ToolResult{Content: "tracker type assertion failed", IsError: true}, nil
	}

	if err := tracker.MarkUnloaded(in.Skill); err != nil {
		t.logger.Info("unload skill",
			zap.String("agent_id", agentRunID),
			zap.String("skill", in.Skill),
			zap.String("outcome", "error"),
			zap.String("reason", err.Error()),
			zap.Int("budget_used", tracker.Count()),
		)
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	t.logger.Info("unload skill",
		zap.String("agent_id", agentRunID),
		zap.String("skill", in.Skill),
		zap.String("outcome", "unloaded"),
		zap.Int("budget_used", tracker.Count()),
	)

	notice := fmt.Sprintf(`<skill-unloaded name="%s" />
请忽略此 skill 的所有先前指令。它的 body 还在历史里，但已不再生效。`, in.Skill)

	return &types.ToolResult{
		Content: fmt.Sprintf("skill %q unloaded; budget now %d/%d", in.Skill, tracker.Count(), tracker.Max()),
		NewMessages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: notice,
				}},
				CreatedAt: time.Now(),
			},
		},
	}, nil
}

const description = `把一个先前 Load 过的 skill 标记为卸载，释放配额。

行为：
- tracker 中该 skill 状态从 active 改为 unloaded，budget -1
- 发出 <skill-unloaded name="..." /> 通知，请你忽略该 skill 的指令
- skill body 仍在 message history 里（LLM API 不允许删历史），只是不再生效

何时用：
- 配额已满（3/3）且必须 LoadSkill 新 skill 时
- candidate 中有 skill 显然不合用，想换个

错误：
- skill 没被 Load 过 → 错误
- skill 已经 unloaded → 错误`
