package plantool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const PlanReadToolName = "plan_read"

type PlanReadTool struct {
	tool.BaseTool
	rootDir string
}

func NewPlanReadTool(rootDir string) *PlanReadTool {
	return &PlanReadTool{rootDir: rootDir}
}

func (*PlanReadTool) Name() string                  { return PlanReadToolName }
func (*PlanReadTool) Description() string           { return planReadDescription }
func (*PlanReadTool) IsReadOnly() bool              { return true }
func (*PlanReadTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (*PlanReadTool) IsEnabled() bool               { return true }
func (*PlanReadTool) IsConcurrencySafe() bool       { return true }

func (*PlanReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "当前 session id（从 spawn-info 获取）",
			},
		},
		"required": []string{"session_id"},
	}
}

type planReadInput struct {
	SessionID string `json:"session_id"`
}

type planReadTask struct {
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	SummaryRef string   `json:"summary_ref,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

type planReadOutput struct {
	Tasks     map[string]planReadTask `json:"tasks"`
	Ready     []string               `json:"ready"`
	AllDone   bool                   `json:"all_done"`
	HasFailed bool                   `json:"has_failed"`
}

func (t *PlanReadTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in planReadInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResultRead("invalid input: " + err.Error()), nil
	}
	if in.SessionID == "" {
		return errResultRead("session_id is required"), nil
	}

	planPath := workspace.PlanPath(t.rootDir, in.SessionID)
	b, err := os.ReadFile(planPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errResultRead(fmt.Sprintf("plan.json not found for session %q", in.SessionID)), nil
		}
		return errResultRead("read plan: " + err.Error()), nil
	}

	var plan workspace.Plan
	if err := json.Unmarshal(b, &plan); err != nil {
		return errResultRead("unmarshal plan: " + err.Error()), nil
	}

	out := planReadOutput{
		Tasks: make(map[string]planReadTask, len(plan.Tasks)),
		Ready: []string{},
	}

	allDone := true
	for id, task := range plan.Tasks {
		out.Tasks[id] = planReadTask{
			Title:      task.Title,
			Status:     string(task.Status),
			SummaryRef: task.SummaryRef,
			DependsOn:  task.DependsOn,
		}
		switch task.Status {
		case workspace.StatusDone, workspace.StatusFailed, workspace.StatusCancelled:
			// terminal
		default:
			allDone = false
		}
		if task.Status == workspace.StatusFailed {
			out.HasFailed = true
		}
	}
	out.AllDone = allDone

	// Compute ready list: pending tasks whose all depends_on are done.
	for id, task := range plan.Tasks {
		if task.Status != workspace.StatusPending {
			continue
		}
		allDepsDone := true
		for _, depID := range task.DependsOn {
			dep, ok := plan.Tasks[depID]
			if !ok || dep.Status != workspace.StatusDone {
				allDepsDone = false
				break
			}
		}
		if allDepsDone {
			out.Ready = append(out.Ready, id)
		}
	}

	result, _ := json.Marshal(out)
	return &types.ToolResult{Content: string(result)}, nil
}

func errResultRead(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: types.ToolErrorInvalidInput}
}

const planReadDescription = `读取当前 session 的 plan.json 执行状态。返回所有任务状态及可立即派发的任务列表。

返回字段：
- tasks: {id: {title, status, summary_ref, depends_on}} — 全量任务快照
- ready: []string — depends_on 全部 done 且自身 pending 的任务 id（可立即 freelance 执行）
- all_done: bool — 所有任务均为 done/failed/cancelled 时为 true
- has_failed: bool — 有任何任务 status=failed 时为 true`
