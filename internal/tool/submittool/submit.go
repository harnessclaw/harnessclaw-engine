// Package submittool implements submit_task_result — the L3-facing
// "I'm done; here is my meta.json" declaration. Under the local-files-
// as-truth model, the meta.json itself is the contract: L3 has already
// committed every output path + summary via meta_write (single-shot
// O_EXCL), and submit_task_result just points L2 at the file.
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

const ToolName = "submit_task_result"

// MetadataRenderHint is the value Execute writes to ToolResult.Metadata
// "render_hint" on success. The driver loop matches on this to flag
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
	// task_id and meta_path are intentionally NOT required: the
	// framework injects task_id via ctx.AgentScope.TaskID and derives
	// meta_path as "tasks/{task_id}/meta.json". LLM-supplied values are
	// accepted as override but callers should leave them empty —
	// framework-known fields shouldn't be in LLM input.
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (*Tool) ValidateInput(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var s submission
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if s.MetaPath != "" && filepath.IsAbs(s.MetaPath) {
		return fmt.Errorf("meta_path must be relative to sessionRoot (got absolute path %q)", s.MetaPath)
	}
	return nil
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var s submission
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return rejected("invalid input: " + err.Error())
		}
	}
	scope, _ := tool.AgentScopeFromCtx(ctx)
	// Fall back to ctx-injected values. LLM-supplied values win so
	// legacy callers and explicit overrides still work.
	if strings.TrimSpace(s.TaskID) == "" {
		s.TaskID = scope.TaskID
	}
	if strings.TrimSpace(s.MetaPath) == "" && s.TaskID != "" {
		s.MetaPath = filepath.Join("tasks", s.TaskID, "meta.json")
	}
	if strings.TrimSpace(s.TaskID) == "" {
		return rejected("task_id missing: framework did not inject TaskID via ctx — engine configuration error")
	}
	if strings.TrimSpace(s.MetaPath) == "" {
		return rejected("meta_path missing and cannot be derived")
	}

	// Resolve meta_path against the spawn's SessionRoot. scope was
	// already pulled above for TaskID/MetaPath fallback; reuse it.
	abs := s.MetaPath
	if !filepath.IsAbs(abs) {
		if scope.SessionRoot == "" {
			return rejected("SessionRoot missing in ctx — engine bug, meta_path cannot be resolved")
		}
		abs = filepath.Join(scope.SessionRoot, s.MetaPath)
	}

	b, err := os.ReadFile(abs)
	if err != nil {
		return rejected(fmt.Sprintf("read meta.json at %s: %v (did meta_write succeed?)", abs, err))
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

	type outputRef struct {
		Path string `json:"path"`
		Type string `json:"type,omitempty"`
	}
	outRefs := make([]outputRef, 0, len(m.Outputs))
	for _, o := range m.Outputs {
		outRefs = append(outRefs, outputRef{Path: o.Path, Type: o.Type})
	}

	body, _ := json.Marshal(struct {
		Status   string      `json:"status"`
		TaskID   string      `json:"task_id"`
		MetaPath string      `json:"meta_path"`
		Summary  string      `json:"summary"`
		Outputs  []outputRef `json:"outputs,omitempty"`
	}{
		Status:   "accepted",
		TaskID:   s.TaskID,
		MetaPath: s.MetaPath,
		Summary:  m.Summary,
		Outputs:  outRefs,
	})
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint":       MetadataRenderHint,
			MetadataKeyAccepted: true,
			"task_id":           s.TaskID,
			"meta_path":         s.MetaPath,
			"summary":           m.Summary,
			"outputs":           outRefs,
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

L2 收到后会读 meta.json 校验 summary 非空 + status + outputs 路径合法，然后 plan_update(status=done, summary_ref=meta_path)。

调用顺序：先 meta_write，再 submit_task_result。
若 meta.json 还没写，本工具会因 ENOENT 拒绝；先写再交。
完全不调本工具就 end_turn 时，L2 会兜底检查 meta.json 是否存在：在则视为完成；不在则视为失败按 D14 重试。

如任务在当前作用域内确实无法完成（缺关键输入 / 约束冲突 / 能力差距），改调 escalate_to_planner——那是"我做不到"的合法出口，不算失败。`
