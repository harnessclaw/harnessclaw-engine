package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

const FinalResultToolName = "browser_agent_final_result"

type FinalResultTool struct {
	tool.BaseTool
	submitter *submittool.Tool
}

type finalResultInput struct {
	Content  string   `json:"content"`
	Source   string   `json:"source"`
	Evidence []string `json:"evidence,omitempty"`
	Notes    string   `json:"notes,omitempty"`
}

func NewFinalResultTool() *FinalResultTool {
	return &FinalResultTool{submitter: submittool.New()}
}

func (t *FinalResultTool) Name() string { return FinalResultToolName }
func (t *FinalResultTool) Description() string {
	return "提交 Browser Agent 最终结果。HarnessClaw 会自动注入当前 task_id 并转交 submit_task_result 校验。"
}
func (t *FinalResultTool) IsReadOnly() bool              { return false }
func (t *FinalResultTool) IsEnabled() bool               { return true }
func (t *FinalResultTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }

func (t *FinalResultTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "最终提取或整理给主 Agent 的内容。",
				"minLength":   1,
			},
			"source": map[string]any{
				"type":        "string",
				"enum":        []string{"browser", "partial"},
				"description": "browser 表示浏览器直接结果；partial 表示浏览器无法完整完成并已说明原因。",
			},
			"evidence": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "可选证据摘要，例如 URL、标题或关键页面事实。",
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "可选说明，例如登录墙、验证码、超时或信息不完整原因。",
			},
		},
		"required": []string{"content", "source"},
	}
}

func (t *FinalResultTool) ValidateInput(raw json.RawMessage) error {
	var in finalResultInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid browser_agent_final_result input: %w", err)
	}
	if strings.TrimSpace(in.Content) == "" {
		return fmt.Errorf("content is required")
	}
	if in.Source != "browser" && in.Source != "partial" {
		return fmt.Errorf("source must be browser or partial")
	}
	return nil
}

func (t *FinalResultTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	taskID := taskIDFromContext(ctx)
	if taskID == "" {
		return &types.ToolResult{
			Content:   "browser_agent_final_result requires a task contract or agent scope task_id",
			IsError:   true,
			ErrorType: types.ToolErrorContractFail,
		}, nil
	}

	var in finalResultInput
	_ = json.Unmarshal(raw, &in)
	result := map[string]any{
		"content": in.Content,
		"source":  in.Source,
	}
	if len(in.Evidence) > 0 {
		result["evidence"] = in.Evidence
	}
	if strings.TrimSpace(in.Notes) != "" {
		result["notes"] = strings.TrimSpace(in.Notes)
	}
	submitInput, _ := json.Marshal(map[string]any{
		"task_id": taskID,
		"result":  result,
	})
	return t.submitter.Execute(ctx, submitInput)
}

func taskIDFromContext(ctx context.Context) string {
	if contract, ok := tool.GetTaskContract(ctx); ok && strings.TrimSpace(contract.TaskID) != "" {
		return strings.TrimSpace(contract.TaskID)
	}
	if scope, ok := tool.AgentScopeFromCtx(ctx); ok && strings.TrimSpace(scope.TaskID) != "" {
		return strings.TrimSpace(scope.TaskID)
	}
	return ""
}
