// Package submittool implements SubmitTaskResult — the L3-facing
// "I'm done; here is my meta.json" declaration. Under the local-files-
// as-truth model, the meta.json itself is the contract: L3 has already
// committed every output path + summary via MetaWrite (single-shot
// O_EXCL), and SubmitTaskResult just points L2 at the file.
//
// Why a separate tool: end_turn alone has no payload, so L2 cannot
// distinguish "L3 finished cleanly" from "L3 ran out of turns". This
// tool gives the loop's terminal a structured signal it can validate.
package submittool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const ToolName = "SubmitTaskResult"

// MetadataRenderHint is the value Execute writes to ToolResult.Metadata
// "render_hint" on success. runSubAgentLoop matches on this to flag
// terminal acceptance — string compare keeps detection O(1).
const MetadataRenderHint = "task_submission"

// MetadataKeyAccepted records whether the submission validated. Both
// branches emit metadata so the loop can count rejections toward retry.
const MetadataKeyAccepted = "submission_accepted"

// submission is the parsed input.
type submission struct {
	TaskID   string `json:"task_id"`
	MetaPath string `json:"meta_path"`
}

// Tool is the L3 task-submission tool.
type Tool struct {
	tool.BaseTool
}

// New returns a fresh tool instance.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string                  { return ToolName }
func (*Tool) Description() string           { return description }
func (*Tool) IsReadOnly() bool              { return false }
func (*Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*Tool) IsEnabled() bool               { return true }
func (*Tool) IsConcurrencySafe() bool       { return false }

func (*Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "本 task 的 id（与 plan.json / meta.json.task_id 一致）。",
			},
			"meta_path": map[string]any{
				"type":        "string",
				"description": "meta.json 路径，相对 sessionRoot。典型形如 tasks/{task_id}/meta.json。",
			},
		},
		"required": []string{"task_id", "meta_path"},
	}
}

func (*Tool) ValidateInput(raw json.RawMessage) error {
	var s submission
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(s.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(s.MetaPath) == "" {
		return fmt.Errorf("meta_path is required")
	}
	if filepath.IsAbs(s.MetaPath) {
		return fmt.Errorf("meta_path must be relative to sessionRoot (got absolute path %q)", s.MetaPath)
	}
	return nil
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var s submission
	if err := json.Unmarshal(raw, &s); err != nil {
		return rejected("invalid input: " + err.Error())
	}
	if strings.TrimSpace(s.TaskID) == "" {
		return rejected("task_id is required")
	}
	if strings.TrimSpace(s.MetaPath) == "" {
		return rejected("meta_path is required")
	}

	// Resolve meta_path against the spawn's SessionRoot. AgentScope is
	// injected by ToolExecutor immediately before Execute; legacy callers
	// (no scope) fall back to the meta_path's literal absolute form via
	// filepath.Clean below.
	scope, _ := tool.AgentScopeFromCtx(ctx)
	abs := s.MetaPath
	if !filepath.IsAbs(abs) {
		if scope.SessionRoot == "" {
			return rejected("SessionRoot missing in ctx — engine bug, meta_path cannot be resolved")
		}
		abs = filepath.Join(scope.SessionRoot, s.MetaPath)
	}

	b, err := os.ReadFile(abs)
	if err != nil {
		return rejected(fmt.Sprintf("read meta.json at %s: %v (did MetaWrite succeed?)", abs, err))
	}
	var m workspace.Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return rejected(fmt.Sprintf("parse meta.json: %v", err))
	}
	if err := m.Validate(); err != nil {
		return rejected("meta invalid: " + err.Error())
	}
	if m.TaskID != s.TaskID {
		return rejected(fmt.Sprintf("meta.task_id (%q) != submitted task_id (%q); meta.json belongs to a different task", m.TaskID, s.TaskID))
	}

	body, _ := json.Marshal(struct {
		Status   string `json:"status"`
		TaskID   string `json:"task_id"`
		MetaPath string `json:"meta_path"`
		Summary  string `json:"summary"`
	}{
		Status:   "accepted",
		TaskID:   s.TaskID,
		MetaPath: s.MetaPath,
		Summary:  m.Summary,
	})
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint":       MetadataRenderHint,
			MetadataKeyAccepted: true,
			"task_id":           s.TaskID,
			"meta_path":         s.MetaPath,
			"summary":           m.Summary,
		},
	}, nil
}

// utf8Len counts runes (not bytes). Shared with EscalateTool so both
// "too long" checks treat Chinese / multibyte text consistently.
func utf8Len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func rejected(reason string) (*types.ToolResult, error) {
	return &types.ToolResult{
		Content:   "Submission rejected: " + reason,
		IsError:   true,
		ErrorType: types.ToolErrorContractFail,
		Metadata: map[string]any{
			"render_hint":       MetadataRenderHint,
			MetadataKeyAccepted: false,
			"reason":            reason,
		},
	}, nil
}

const description = `通知 L2: "我已写完 meta.json + 所有 outputs[].path 声明的产物，请验收。"

- task_id: 自己的 task id（与 plan.json / meta.json.task_id 一致）。
- meta_path: meta.json 的相对 sessionRoot 路径（典型形如 tasks/{task_id}/meta.json）。

L2 收到后会读 meta.json 校验 summary 非空 + status + outputs 路径合法，然后 PlanUpdate(status=done, summary_ref=meta_path)。

调用顺序：先 MetaWrite，再 SubmitTaskResult。
若 meta.json 还没写，本工具会因 ENOENT 拒绝；先写再交。
完全不调本工具就 end_turn 时，L2 会兜底检查 meta.json 是否存在：在则视为完成；不在则视为失败按 D14 重试。

如任务在当前作用域内确实无法完成（缺关键输入 / 约束冲突 / 能力差距），改调 EscalateToPlanner——那是"我做不到"的合法出口，不算失败。`
