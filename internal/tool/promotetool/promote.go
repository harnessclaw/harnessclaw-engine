// Package promotetool implements the Promote tool — L2's "this is a final
// deliverable, surface it to the user" action. Uses cp (not mv): source is
// frozen by the same call, so the two copies never diverge.
package promotetool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const ToolName = "Promote"

type PromoteTool struct {
	tool.BaseTool
	registry *workspace.PlanWriterRegistry
	rootDir  string
	events   chan<- types.EngineEvent // nil OK; tests/headless skip emit
}

func NewPromoteTool(registry *workspace.PlanWriterRegistry, rootDir string, events chan<- types.EngineEvent) *PromoteTool {
	return &PromoteTool{registry: registry, rootDir: rootDir, events: events}
}

func (*PromoteTool) Name() string                  { return ToolName }
func (*PromoteTool) Description() string           { return description }
func (*PromoteTool) IsReadOnly() bool              { return false }
func (*PromoteTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*PromoteTool) IsEnabled() bool               { return true }
func (*PromoteTool) IsConcurrencySafe() bool       { return true }

func (*PromoteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{"type": "string"},
			"task_id":    map[string]any{"type": "string"},
			"mappings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"output_index": map[string]any{"type": "integer", "minimum": 0},
						"target_name":  map[string]any{"type": "string"},
					},
					"required": []string{"output_index", "target_name"},
				},
				"minItems": 1,
			},
		},
		"required": []string{"session_id", "task_id", "mappings"},
	}
}

type input struct {
	SessionID string    `json:"session_id"`
	TaskID    string    `json:"task_id"`
	Mappings  []mapping `json:"mappings"`
}

type mapping struct {
	OutputIndex int    `json:"output_index"`
	TargetName  string `json:"target_name"`
}

func (t *PromoteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	// PreCheck stage — read meta + plan, validate every mapping before any
	// filesystem mutation. Cheaper to refuse here than to copy-then-rollback.
	meta, err := loadMeta(t.rootDir, in.SessionID, in.TaskID)
	if err != nil {
		return errResult("load meta: " + err.Error()), nil
	}
	plan, err := loadPlan(t.rootDir, in.SessionID)
	if err != nil {
		return errResult("load plan: " + err.Error()), nil
	}
	task, ok := plan.Tasks[in.TaskID]
	if !ok {
		return errResult(fmt.Sprintf("task %s not found in plan", in.TaskID)), nil
	}
	if task.Frozen {
		return errResult(fmt.Sprintf("task %s already promoted (frozen); Promote is single-shot", in.TaskID)), nil
	}
	seen := map[int]bool{}
	seenTarget := map[string]bool{}
	deliverDir := workspace.DeliverablesDir(t.rootDir, in.SessionID)
	for _, m := range in.Mappings {
		if m.OutputIndex < 0 || m.OutputIndex >= len(meta.Outputs) {
			return errResult(fmt.Sprintf("output_index %d out of range [0,%d)", m.OutputIndex, len(meta.Outputs))), nil
		}
		if seen[m.OutputIndex] {
			return errResult(fmt.Sprintf("duplicate output_index %d", m.OutputIndex)), nil
		}
		seen[m.OutputIndex] = true
		if seenTarget[m.TargetName] {
			return errResult(fmt.Sprintf("duplicate target_name %q", m.TargetName)), nil
		}
		seenTarget[m.TargetName] = true
		src := meta.Outputs[m.OutputIndex].Path
		if _, err := os.Stat(src); err != nil {
			return errResult(fmt.Sprintf("source %s not accessible: %v", src, err)), nil
		}
		dst := filepath.Join(deliverDir, m.TargetName)
		if _, err := os.Stat(dst); err == nil {
			return errResult(fmt.Sprintf("target %s already exists; pick a different target_name", dst)), nil
		}
	}

	// Copy stage — io.Copy each mapping. Track copied files so PlanWriter
	// failure can roll them back atomically.
	var copied []string
	rollback := func() {
		for _, p := range copied {
			_ = os.Remove(p)
		}
	}
	for _, m := range in.Mappings {
		src := meta.Outputs[m.OutputIndex].Path
		dst := filepath.Join(deliverDir, m.TargetName)
		if err := copyFile(src, dst); err != nil {
			rollback()
			return errResult(fmt.Sprintf("copy %s → %s: %v", src, dst, err)), nil
		}
		copied = append(copied, dst)
	}

	// PlanWriter mutation — freeze task + append Deliverable entries. If
	// this fails (e.g. plan.json corrupted between PreCheck and now), we
	// roll back the copied files so the filesystem stays consistent with
	// the unchanged plan.
	w := t.registry.Get(in.SessionID)
	now := time.Now().UTC()
	err = w.Apply(ctx, func(p *workspace.Plan) error {
		task, ok := p.Tasks[in.TaskID]
		if !ok {
			return fmt.Errorf("task %s vanished from plan", in.TaskID)
		}
		if task.Frozen {
			return fmt.Errorf("task %s already frozen", in.TaskID)
		}
		task.Frozen = true
		for _, m := range in.Mappings {
			p.Deliverables = append(p.Deliverables, workspace.DeliverableEntry{
				Path:         "deliverables/" + m.TargetName,
				PromotedFrom: meta.Outputs[m.OutputIndex].Path,
				PromotedAt:   now,
			})
		}
		return nil
	})
	if err != nil {
		rollback()
		return errResult("plan update: " + err.Error()), nil
	}

	// Best-effort Deliverable events. Non-blocking — UI can also recover
	// from plan.json on reconnect, so a dropped event is not load-bearing.
	if t.events != nil {
		for _, m := range in.Mappings {
			dst := filepath.Join(deliverDir, m.TargetName)
			select {
			case t.events <- types.EngineEvent{
				Type:        types.EngineEventDeliverable,
				Deliverable: &types.Deliverable{FilePath: dst, ByteSize: int(safeSize(dst))},
			}:
			default:
			}
		}
	}

	return &types.ToolResult{
		Content: fmt.Sprintf("promoted %d file(s) for task %s; task is now frozen", len(in.Mappings), in.TaskID),
	}, nil
}

func loadMeta(root, sid, tid string) (*workspace.Meta, error) {
	b, err := os.ReadFile(workspace.MetaPath(root, sid, tid))
	if err != nil {
		return nil, err
	}
	var m workspace.Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func loadPlan(root, sid string) (*workspace.Plan, error) {
	b, err := os.ReadFile(workspace.PlanPath(root, sid))
	if err != nil {
		return nil, err
	}
	var p workspace.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(df, sf); err != nil {
		_ = df.Close()
		_ = os.Remove(dst)
		return err
	}
	return df.Close()
}

func safeSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: types.ToolErrorInvalidInput}
}

const description = `把 task 的产出文件 promote 到 deliverables/，即向用户曝光为成品。仅 L2 调用。

- 一次调用支持多个 mapping（{output_index, target_name}）。
- 用 cp 不是 mv：源文件保留在 tasks/ 不动。
- 调用即整个 task frozen（不可再 promote、不可再改 status）。
- target_name 在 deliverables/ 下必须不重名。
- 失败回滚：任一阶段失败，已 cp 的目标文件被 os.Remove 清理。`
