package loadskill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const ToolName = "load_skill"

// maxBodyBytes caps a single skill's SKILL.md body to keep prompt size
// bounded — spec §6 hard limit.
const maxBodyBytes = 100 * 1024

type LoadSkillTool struct {
	tool.BaseTool
	reader *skill.Reader
	logger *zap.Logger
}

func New(reader *skill.Reader, logger *zap.Logger) *LoadSkillTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LoadSkillTool{reader: reader, logger: logger}
}

func (t *LoadSkillTool) Name() string        { return ToolName }
func (t *LoadSkillTool) Description() string { return description }
func (t *LoadSkillTool) IsReadOnly() bool    { return false }

func (t *LoadSkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"skill"},
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "要加载的 skill 名字（search_skill 返回结果里的 name 字段）。",
			},
		},
	}
}

type input struct {
	Skill string `json:"skill"`
}

func (t *LoadSkillTool) ValidateInput(raw json.RawMessage) error {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if in.Skill == "" {
		return fmt.Errorf("skill is required")
	}
	return nil
}

func (t *LoadSkillTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		t.logInfo(agentRunID, "", "invalid_input", err.Error(), 0, 0, "")
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	trackerVal, ok := tool.GetSkillTrackerValue(ctx)
	if !ok {
		t.logInfo(agentRunID, in.Skill, "tracker_missing", "non-freelancer caller", 0, 0, "")
		return &types.ToolResult{
			Content: "load_skill is only available to freelancer sub-agents",
			IsError: true,
		}, nil
	}
	tracker, ok := trackerVal.(*loop.SkillTracker)
	if !ok {
		t.logInfo(agentRunID, in.Skill, "tracker_type_error", "", 0, 0, "")
		return &types.ToolResult{Content: "tracker type assertion failed", IsError: true}, nil
	}

	// Branch 1: already active → idempotent no-op, don't re-emit body.
	if tracker.IsActive(in.Skill) {
		t.logInfo(agentRunID, in.Skill, "idempotent", "", 0, tracker.Count(), "")
		return &types.ToolResult{
			Content: fmt.Sprintf("skill %q already active", in.Skill),
		}, nil
	}

	// Branch 2: tracked but unloaded → reactivate (with budget pre-check).
	if tracker.IsTracked(in.Skill) {
		if tracker.Count() >= tracker.Max() {
			t.logInfo(agentRunID, in.Skill, "denied", "budget_full", 0, tracker.Count(), "")
			active, _ := tracker.List()
			return &types.ToolResult{
				Content: fmt.Sprintf("skill budget full (%d/%d), active: %v — unload_skill one first",
					tracker.Count(), tracker.Max(), nameList(active)),
				IsError: true,
			}, nil
		}
		if err := tracker.Reactivate(in.Skill); err != nil {
			t.logInfo(agentRunID, in.Skill, "error", "reactivate_failed: "+err.Error(), 0, tracker.Count(), "")
			return &types.ToolResult{Content: "reactivate failed: " + err.Error(), IsError: true}, nil
		}
		full, _ := tracker.GetFull(in.Skill)
		version := ""
		bodyBytes := 0
		if full != nil {
			version = full.Version
			bodyBytes = len(full.Body)
		}
		t.logInfo(agentRunID, in.Skill, "reactivated", "", bodyBytes, tracker.Count(), version)
		return reInjectMessage(in.Skill, full), nil
	}

	// Branch 3: new skill — budget pre-check, read disk, size check, add.
	if tracker.Count() >= tracker.Max() {
		t.logInfo(agentRunID, in.Skill, "denied", "budget_full", 0, tracker.Count(), "")
		active, _ := tracker.List()
		return &types.ToolResult{
			Content: fmt.Sprintf("skill budget full (%d/%d), active: %v — unload_skill one first",
				tracker.Count(), tracker.Max(), nameList(active)),
			IsError: true,
		}, nil
	}
	if t.reader == nil {
		t.logInfo(agentRunID, in.Skill, "error", "reader_unset", 0, tracker.Count(), "")
		return &types.ToolResult{Content: "skill reader not configured", IsError: true}, nil
	}
	full, err := t.reader.Load(in.Skill)
	if err != nil {
		t.logInfo(agentRunID, in.Skill, "error", "disk_load_failed: "+err.Error(), 0, tracker.Count(), "")
		return &types.ToolResult{Content: "load failed: " + err.Error(), IsError: true}, nil
	}
	if len(full.Body) > maxBodyBytes {
		t.logInfo(agentRunID, in.Skill, "error", "oversize", len(full.Body), tracker.Count(), full.Version)
		return &types.ToolResult{
			Content: fmt.Sprintf("skill body %d bytes exceeds %d-byte limit", len(full.Body), maxBodyBytes),
			IsError: true,
		}, nil
	}
	if err := tracker.Add(full); err != nil {
		t.logInfo(agentRunID, in.Skill, "error", "tracker_add_failed: "+err.Error(), len(full.Body), tracker.Count(), full.Version)
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	t.logInfo(agentRunID, in.Skill, "loaded", "", len(full.Body), tracker.Count(), full.Version)
	return reInjectMessage(in.Skill, full), nil
}

// logInfo emits the single end-of-Execute INFO line for load_skill. Every
// branch funnels through it so operators see one record per load_skill call
// with the same field schema regardless of outcome.
func (t *LoadSkillTool) logInfo(agentRunID, skillName, outcome, reason string, bodyBytes, budgetUsed int, version string) {
	fields := []zap.Field{
		zap.String("agent_id", agentRunID),
		zap.String("skill", skillName),
		zap.String("outcome", outcome),
		zap.Int("budget_used", budgetUsed),
	}
	if version != "" {
		fields = append(fields, zap.String("version", version))
	}
	if bodyBytes > 0 {
		fields = append(fields, zap.Int("body_bytes", bodyBytes))
	}
	if reason != "" {
		fields = append(fields, zap.String("reason", reason))
	}
	t.logger.Info("load skill", fields...)
}

func reInjectMessage(name string, full *skill.SkillFull) *types.ToolResult {
	block := engine.BuildSingleSkillBlock(full)
	return &types.ToolResult{
		Content: fmt.Sprintf("skill %q loaded", name),
		NewMessages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: block,
				}},
				CreatedAt: time.Now(),
			},
		},
	}
}

func nameList(refs []loop.LoadedRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

const description = `把指定的 skill 加载到当前任务上下文。

行为：
- 把 SKILL.md body 作为新的 user message 注入（含 <skill name="..." version="..." root="..."> XML 包裹）
- LLM 在下一轮就能看到 skill 内容并按 body 指令工作
- root 属性是 skill 在磁盘上的根目录，可用于 Bash 调 {root}/scripts/...

约束：
- 上下文中并存 skill 数量上限 3（含 L2 预分配的 candidate）
- 已加载且 active → 幂等
- 已加载但 unloaded → 重新激活，仍受 budget 约束
- 配额满 → 失败；先 unload_skill 一个再 Load 新的
- body > 100KB → 拒绝`
