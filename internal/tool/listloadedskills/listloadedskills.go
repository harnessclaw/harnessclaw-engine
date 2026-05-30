package listloadedskills

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/skill/tracker"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const ToolName = "list_loaded_skills"

type ListLoadedSkillsTool struct {
	tool.BaseTool
	logger *zap.Logger
}

func New(logger *zap.Logger) *ListLoadedSkillsTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ListLoadedSkillsTool{logger: logger}
}

func (t *ListLoadedSkillsTool) Name() string         { return ToolName }
func (t *ListLoadedSkillsTool) Description() string  { return description }
func (t *ListLoadedSkillsTool) IsReadOnly() bool     { return true }
func (t *ListLoadedSkillsTool) IsConcurrencySafe() bool { return true }

func (t *ListLoadedSkillsTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

type budget struct {
	Used int `json:"used"`
	Max  int `json:"max"`
}

type output struct {
	Active   []tracker.LoadedRef `json:"active"`
	Unloaded []tracker.LoadedRef `json:"unloaded"`
	Budget   budget             `json:"budget"`
}

func (t *ListLoadedSkillsTool) Execute(ctx context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	trackerVal, ok := tool.GetSkillTrackerValue(ctx)
	if !ok {
		t.logger.Info("list loaded skills",
			zap.String("agent_id", agentRunID),
			zap.String("outcome", "tracker_missing"),
			zap.String("reason", "non-freelancer caller"),
		)
		return &types.ToolResult{
			Content: "list_loaded_skills is only available to freelancer sub-agents",
			IsError: true,
		}, nil
	}
	tr, ok := trackerVal.(*tracker.SkillTracker)
	if !ok {
		t.logger.Info("list loaded skills",
			zap.String("agent_id", agentRunID),
			zap.String("outcome", "tracker_type_error"),
		)
		return &types.ToolResult{Content: "tracker type assertion failed", IsError: true}, nil
	}

	active, unloaded := tr.List()
	if active == nil {
		active = []tracker.LoadedRef{}
	}
	if unloaded == nil {
		unloaded = []tracker.LoadedRef{}
	}
	out := output{
		Active:   active,
		Unloaded: unloaded,
		Budget:   budget{Used: tr.Count(), Max: tr.Max()},
	}
	body, _ := json.Marshal(out)

	activeNames := make([]string, 0, len(active))
	for _, r := range active {
		activeNames = append(activeNames, r.Name)
	}
	unloadedNames := make([]string, 0, len(unloaded))
	for _, r := range unloaded {
		unloadedNames = append(unloadedNames, r.Name)
	}
	t.logger.Info("list loaded skills",
		zap.String("agent_id", agentRunID),
		zap.String("outcome", "ok"),
		zap.Strings("active", activeNames),
		zap.Strings("unloaded", unloadedNames),
		zap.Int("budget_used", tr.Count()),
		zap.Int("budget_max", tr.Max()),
	)
	return &types.ToolResult{Content: string(body)}, nil
}

const description = `列出 freelancer 当前装载的所有 skill 状态。

返回：
- active: 当前生效的 skill 列表（计入 budget）
- unloaded: 已 Unload 但 body 还在历史里的 skill
- budget: {used, max}

何时用：
- 配额满了想知道应该 unload 哪个
- 想确认某个 skill 是不是真的还在生效（active vs unloaded）
- 任务复盘时检查用过哪些 skill`
