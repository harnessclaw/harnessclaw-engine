// Package plantool implements the plan_update LLM tool — the sole L2-facing
// interface for mutating plan.json. All mutations flow through PlanWriter
// (single-consumer goroutine per session) so the state machine is never raced.
package plantool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const ToolName = "plan_update"

type PlanUpdateTool struct {
	tool.BaseTool
	registry *workspace.PlanWriterRegistry
	rootDir  string
}

func NewPlanUpdateTool(registry *workspace.PlanWriterRegistry, rootDir string) *PlanUpdateTool {
	return &PlanUpdateTool{registry: registry, rootDir: rootDir}
}

func (*PlanUpdateTool) Name() string                  { return ToolName }
func (*PlanUpdateTool) Description() string           { return description }
func (*PlanUpdateTool) IsReadOnly() bool              { return false }
func (*PlanUpdateTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*PlanUpdateTool) IsEnabled() bool               { return true }
func (*PlanUpdateTool) IsConcurrencySafe() bool       { return true }

func (*PlanUpdateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op": map[string]any{
				"type":        "string",
				"enum":        []string{"create_task", "update_status", "wipe_for_retry"},
				"description": "本次操作。create_task 新增 task 条目并 mkdir 其目录；update_status 改 status 并可附带 summary_ref；wipe_for_retry 清空 task 目录 + attempt++ + status=pending。",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "可选：框架从 ctx 注入；通常不传。仅在需要跨 session 操作时显式覆盖。",
			},
			"task": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string"},
					"title":       map[string]any{"type": "string"},
					"agent":       map[string]any{"type": "string"},
					"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"input_paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"description": "仅 op=create_task 时使用。",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "仅 op=update_status/wipe_for_retry 时使用。",
			},
			"status": map[string]any{
				"type":        "string",
				"enum":        []string{"pending", "running", "done", "failed", "cancelled"},
				"description": "仅 op=update_status 时使用。",
			},
			"summary_ref": map[string]any{
				"type":        "string",
				"description": "可选；当 status=done 时**必须**指向有效 meta.json。相对 sessionRoot。",
			},
		},
		// session_id is intentionally NOT required — see field description.
		"required": []string{"op"},
	}
}

type input struct {
	Op         string     `json:"op"`
	SessionID  string     `json:"session_id"`
	Task       *taskInput `json:"task,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	Status     string     `json:"status,omitempty"`
	SummaryRef string     `json:"summary_ref,omitempty"`
}

type taskInput struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Agent      string   `json:"agent"`
	DependsOn  []string `json:"depends_on"`
	InputPaths []string `json:"input_paths"`
}

func (t *PlanUpdateTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}
	// Fall back to ctx-injected SessionRoot. LLM-supplied value (when
	// present) takes precedence so legacy callers and cross-session
	// overrides still work.
	if in.SessionID == "" {
		if scope, ok := tool.AgentScopeFromCtx(ctx); ok && scope.SessionRoot != "" {
			in.SessionID = filepath.Base(scope.SessionRoot)
		}
	}
	if in.SessionID == "" {
		return errResult("session_id missing: framework did not inject SessionRoot via ctx — engine configuration error"), nil
	}
	w := t.registry.Get(in.SessionID)

	switch in.Op {
	case "create_task":
		return t.handleCreate(ctx, w, &in)
	case "update_status":
		return t.handleUpdateStatus(ctx, w, &in)
	case "wipe_for_retry":
		return t.handleWipe(ctx, w, &in)
	default:
		return errResult(fmt.Sprintf("unknown op %q", in.Op)), nil
	}
}

func (t *PlanUpdateTool) handleCreate(ctx context.Context, w *workspace.PlanWriter, in *input) (*types.ToolResult, error) {
	if in.Task == nil || in.Task.ID == "" {
		return errResult("op=create_task requires task.id"), nil
	}
	taskDir := workspace.TaskDir(t.rootDir, in.SessionID, in.Task.ID)
	if err := workspace.EnsureTaskDir(t.rootDir, in.SessionID, in.Task.ID); err != nil {
		return errResult("mkdir task dir: " + err.Error()), nil
	}
	var snapshot *workspace.Plan
	err := w.Apply(ctx, func(p *workspace.Plan) error {
		if p.Tasks == nil {
			p.Tasks = map[string]*workspace.Task{}
		}
		if _, exists := p.Tasks[in.Task.ID]; exists {
			return fmt.Errorf("task %q already exists", in.Task.ID)
		}
		p.Tasks[in.Task.ID] = &workspace.Task{
			Title:      in.Task.Title,
			Agent:      in.Task.Agent,
			Status:     workspace.StatusPending,
			Attempt:    1,
			DependsOn:  in.Task.DependsOn,
			InputPaths: in.Task.InputPaths,
			OutputDir:  fmt.Sprintf("tasks/%s/", in.Task.ID),
		}
		snapshot = p
		return nil
	})
	if err != nil {
		// Roll back the empty dir we created so filesystem stays in sync
		// with plan.json. If removal fails, surface both errors but keep
		// the leftover dir — refusing the call is more important than the
		// stray directory.
		if removeErr := os.Remove(taskDir); removeErr != nil && !os.IsNotExist(removeErr) {
			err = fmt.Errorf("%w (and rmdir rollback failed: %v)", err, removeErr)
		}
		return errResult("plan update: " + err.Error()), nil
	}
	evtType := types.EngineEventPlanUpdated
	if len(snapshot.Tasks) == 1 {
		evtType = types.EngineEventPlanCreated
	}
	emitPlanEvent(ctx, evtType, snapshot)
	return okResult(fmt.Sprintf("task %s created", in.Task.ID)), nil
}

func (t *PlanUpdateTool) handleUpdateStatus(ctx context.Context, w *workspace.PlanWriter, in *input) (*types.ToolResult, error) {
	if in.TaskID == "" {
		return errResult("op=update_status requires task_id"), nil
	}
	st := workspace.Status(in.Status)
	if !st.Valid() {
		return errResult(fmt.Sprintf("status %q invalid", in.Status)), nil
	}
	if st == workspace.StatusDone && in.SummaryRef == "" {
		return errResult("status=done requires summary_ref"), nil
	}
	var snapshot *workspace.Plan
	err := w.Apply(ctx, func(p *workspace.Plan) error {
		task, ok := p.Tasks[in.TaskID]
		if !ok {
			return fmt.Errorf("task %q not found", in.TaskID)
		}
		if task.Frozen {
			return fmt.Errorf("task %q is frozen — cannot update status", in.TaskID)
		}
		task.Status = st
		if in.SummaryRef != "" {
			task.SummaryRef = in.SummaryRef
		}
		if st == workspace.StatusRunning && task.StartedAt.IsZero() {
			task.StartedAt = time.Now().UTC()
		}
		if st == workspace.StatusDone || st == workspace.StatusFailed {
			task.EndedAt = time.Now().UTC()
		}
		snapshot = p
		return nil
	})
	if err != nil {
		return errResult("plan update: " + err.Error()), nil
	}
	emitPlanEvent(ctx, planOverallEventType(snapshot), snapshot)
	return okResult(fmt.Sprintf("task %s status=%s", in.TaskID, in.Status)), nil
}

func (t *PlanUpdateTool) handleWipe(ctx context.Context, w *workspace.PlanWriter, in *input) (*types.ToolResult, error) {
	if in.TaskID == "" {
		return errResult("op=wipe_for_retry requires task_id"), nil
	}
	if err := workspace.WipeTaskDir(t.rootDir, in.SessionID, in.TaskID); err != nil {
		return errResult("wipe dir: " + err.Error()), nil
	}
	var newAttempt int
	var snapshot *workspace.Plan
	err := w.Apply(ctx, func(p *workspace.Plan) error {
		task, ok := p.Tasks[in.TaskID]
		if !ok {
			return fmt.Errorf("task %q not found", in.TaskID)
		}
		if task.Frozen {
			return fmt.Errorf("task %q is frozen — cannot retry", in.TaskID)
		}
		task.Attempt++
		task.Status = workspace.StatusPending
		task.SummaryRef = ""
		task.StartedAt = time.Time{}
		task.EndedAt = time.Time{}
		newAttempt = task.Attempt
		snapshot = p
		return nil
	})
	if err != nil {
		return errResult("plan update: " + err.Error()), nil
	}
	emitPlanEvent(ctx, types.EngineEventPlanUpdated, snapshot)
	return okResult(fmt.Sprintf("task %s wiped; attempt now %d", in.TaskID, newAttempt)), nil
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: types.ToolErrorInvalidInput}
}

func okResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg}
}

// planOverallEventType returns the plan-level event type based on whether all
// tasks have reached a terminal state.
func planOverallEventType(p *workspace.Plan) types.EngineEventType {
	hasFailed := false
	for _, t := range p.Tasks {
		switch t.Status {
		case workspace.StatusPending, workspace.StatusRunning:
			return types.EngineEventPlanUpdated
		case workspace.StatusFailed:
			hasFailed = true
		}
	}
	if hasFailed {
		return types.EngineEventPlanFailed
	}
	return types.EngineEventPlanCompleted
}

// emitPlanEvent sends a non-blocking plan lifecycle event on the context
// event channel. Dropped events are acceptable — the client can recover from
// plan.json on reconnect.
func emitPlanEvent(ctx context.Context, evtType types.EngineEventType, p *workspace.Plan) {
	out, ok := tool.GetEventOut(ctx)
	if !ok {
		return
	}
	tasks := make([]types.PlanTaskInfo, 0, len(p.Tasks))
	for id, t := range p.Tasks {
		tasks = append(tasks, types.PlanTaskInfo{
			TaskID:          id,
			SubagentType:    t.Agent,
			DependsOn:       t.DependsOn,
			UserFacingTitle: t.Title,
		})
	}
	select {
	case out <- types.EngineEvent{
		Type: evtType,
		PlanEvent: &types.PlanEvent{
			PlanID: p.SessionID,
			Status: planEventStatus(evtType),
			Tasks:  tasks,
		},
	}:
	default:
	}
}

func planEventStatus(evtType types.EngineEventType) string {
	switch evtType {
	case types.EngineEventPlanCreated:
		return "created"
	case types.EngineEventPlanCompleted:
		return "completed"
	case types.EngineEventPlanFailed:
		return "failed"
	default:
		return "updated"
	}
}

const description = `维护 plan.json 状态机的唯一入口。只允许 L2 调用。

支持的 op：
- create_task: 新建 task 条目，原子捆绑 mkdir task 目录。需要 task.{id,title,agent}。
- update_status: 改 task 的 status（pending/running/done/failed/cancelled）。status=done 必须带 summary_ref 指向 meta.json。
- wipe_for_retry: 重试用。清空 task 目录 + attempt++ + status=pending。frozen task 拒绝。

所有 mutation 走 PlanWriter 单 consumer goroutine 串行化，跨 mutation 安全。
session_id 必须显式传入（防 LLM 跨 session 误改）。`
