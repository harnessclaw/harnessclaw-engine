// Package promotetool implements the promote tool — emma's "this sub-agent
// output is good enough for the user, lift it to deliverables/" action.
//
// Workflow:
//
//	sub-agent: write artifacts → meta_write → submit_task_result
//	   ↓ (artifacts stay in tasks/t-xxx/)
//	emma: dispatch returns metadata.task_id + outputs[]
//	   ↓ (emma judges quality)
//	emma: promote({task_id, promotions: [{source, as?}]})
//	   ↓
//	   deliverables/report.md   (默认保持原名)
//	   ↓
//	emma tells user "成品在 deliverables/report.md"
//
// Design notes:
//   - Caller: emma main agent (not sub-agent). Sub-agents only know about
//     their own task_dir; emma is the curator deciding which outputs are
//     user-facing.
//   - Pure file operation: no plan.json mutation, no meta.json read. The
//     legacy plan-mode coupling (task registration / frozen flag /
//     PlanWriter) was removed when emma started dispatching L3 directly.
//   - **Filename is user-facing**: 默认保持源文件名 (报告就叫 report.md);
//     deliverables/ 下同名冲突时 promote 报错让 LLM 自己起一个可读性的
//     新名 (例如 report_v2.md / report_q4_2026.md), 而不是机械加 task_id
//     前缀污染用户视角的命名。
//   - Uses cp not mv: source file remains in task_dir so retries / replays
//     can read it again; deliverables/ holds the curated snapshot.
package promotetool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"harnessclaw-go/internal/metric/sessionstats"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const ToolName = "promote"

// PromoteTool 把 task_dir 的产物拷到 deliverables/。emma palette 工具。
type PromoteTool struct {
	tool.BaseTool
	rootDir string
	events  chan<- types.EngineEvent // nil OK; tests/headless skip emit
}

// NewPromoteTool constructs a PromoteTool rooted at rootDir.
//
// 注：旧 NewPromoteTool 接受 PlanWriterRegistry 参数 (plan.json mutation)，
// 现在 promote 不再操作 plan.json，参数砍到只剩 rootDir + events。
func NewPromoteTool(rootDir string, events chan<- types.EngineEvent) *PromoteTool {
	return &PromoteTool{rootDir: rootDir, events: events}
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
			"task_id": map[string]any{
				"type":        "string",
				"description": "要 promote 的 sub-agent task_id（从 dispatch 工具返回 metadata 的 task_id 字段拿）。例：t-7f21d356-8ee",
			},
			"promotions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source": map[string]any{
							"type":        "string",
							"description": "task_dir 内的源文件名（basename，不含 \"/\"）。例：report.md / plan.md",
						},
						"as": map[string]any{
							"type":        "string",
							"description": "可选。deliverables/ 下的目标文件名（basename）。**默认保持源文件名 (推荐)**；只有当 deliverables/ 下已经有同名文件、需要避免覆盖时，才显式起一个**有可读性、用户能理解**的新名，例如：report_v2.md / q4_sales_report.md / design_oauth_2026-06-17.md。绝不要起 file_1.md / output_a.md 这种没语义的占位名。",
						},
					},
					"required": []string{"source"},
				},
				"minItems":    1,
				"description": "要 promote 的文件列表。默认每项保持源文件名；同名冲突时通过 as 起一个用户能理解的新名。",
			},
		},
		"required": []string{"task_id", "promotions"},
	}
}

type input struct {
	TaskID     string      `json:"task_id"`
	Promotions []promotion `json:"promotions"`
}

type promotion struct {
	Source string `json:"source"`
	As     string `json:"as,omitempty"`
}

// promotedItem 是单个 promote 文件的结果，返回给 emma 的 metadata 里。
type promotedItem struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

func (t *PromoteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return errResult("task_id is required"), nil
	}
	if len(in.Promotions) == 0 {
		return errResult("promotions must contain at least one entry"), nil
	}

	// session_id 从 ctx 自取 —— emma 主路径会注入 RootSessionID
	// (emma/core.go ProcessMessage)。LLM 不应该传 session_id, 也没法传对。
	sessionID, ok := sessionstats.RootSessionIDFromCtx(ctx)
	if !ok || sessionID == "" {
		return errResult("session_id missing: framework did not inject RootSessionID via ctx — engine configuration error"), nil
	}
	if t.rootDir == "" {
		return errResult("rootDir not configured — promote disabled"), nil
	}

	taskDir := workspace.TaskDir(t.rootDir, sessionID, in.TaskID)
	if _, err := os.Stat(taskDir); err != nil {
		return errResult(fmt.Sprintf("task_dir %s not accessible: %v (did the sub-agent run yet?)", taskDir, err)), nil
	}

	// PreCheck stage —— 校验每个 promotion 合法 + 源文件存在 + 目标不重名。
	// 预检失败不动文件系统，让 LLM 一次性看到所有问题再纠正。
	deliverDir := workspace.DeliverablesDir(t.rootDir, sessionID)
	if err := os.MkdirAll(deliverDir, 0o755); err != nil {
		return errResult("create deliverables dir: " + err.Error()), nil
	}

	type plan struct{ src, dst, dstBase string }
	plans := make([]plan, 0, len(in.Promotions))
	seenSource := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, p := range in.Promotions {
		// source 校验：必填 + basename only
		if err := validateBasename(p.Source); err != nil {
			return errResult(fmt.Sprintf("source %q invalid: %v", p.Source, err)), nil
		}
		if seenSource[p.Source] {
			return errResult(fmt.Sprintf("duplicate source %q in one promote call", p.Source)), nil
		}
		seenSource[p.Source] = true

		// as 校验：可选，给了就要合法
		dstName := p.Source
		if p.As != "" {
			if err := validateBasename(p.As); err != nil {
				return errResult(fmt.Sprintf("as %q invalid: %v", p.As, err)), nil
			}
			dstName = p.As
		}
		if seenTarget[dstName] {
			return errResult(fmt.Sprintf("duplicate target name %q in one promote call (两条 promotion 指向同一目标名)", dstName)), nil
		}
		seenTarget[dstName] = true

		src := filepath.Join(taskDir, p.Source)
		if _, err := os.Stat(src); err != nil {
			return errResult(fmt.Sprintf("source %s not accessible: %v", src, err)), nil
		}
		dst := filepath.Join(deliverDir, dstName)
		if _, err := os.Stat(dst); err == nil {
			return errResult(fmt.Sprintf(
				"deliverables/%s 已存在 — 同名冲突。请在 promotions 里给这一项加 \"as\" 字段，起一个**用户能理解、有可读性后缀**的新名（例：report_v2.md / q4_sales_report.md / design_oauth_2026-06-17.md），不要用 file_1.md / output_a.md 这种没语义的占位名。",
				dstName,
			)), nil
		}
		plans = append(plans, plan{src: src, dst: dst, dstBase: dstName})
	}

	// Copy stage —— 逐个拷贝。任一失败就 rollback 已拷的，文件系统保持
	// "全成功 or 全不动" 的原子性。
	var copied []string
	rollback := func() {
		for _, p := range copied {
			_ = os.Remove(p)
		}
	}
	promoted := make([]promotedItem, 0, len(plans))
	for _, p := range plans {
		if err := copyFile(p.src, p.dst); err != nil {
			rollback()
			return errResult(fmt.Sprintf("copy %s → %s: %v", p.src, p.dst, err)), nil
		}
		copied = append(copied, p.dst)
		promoted = append(promoted, promotedItem{Source: p.src, Path: p.dst})
	}

	// 发 Deliverable 事件 —— UI 前端收到后可在 deliverables 区显示新成品。
	// 非阻塞 (best-effort)，event channel 满或缺失都不影响 promote 完成。
	out := t.events
	if out == nil {
		if ch, ok := tool.GetEventOut(ctx); ok {
			out = ch
		}
	}
	if out != nil {
		for _, item := range promoted {
			select {
			case out <- types.EngineEvent{
				Type: types.EngineEventDeliverable,
				Deliverable: &types.Deliverable{
					FilePath: item.Path,
					ByteSize: int(safeSize(item.Path)),
				},
			}:
			default:
			}
		}
	}

	body, _ := json.Marshal(struct {
		Status    string         `json:"status"`
		TaskID    string         `json:"task_id"`
		Promoted  []promotedItem `json:"promoted"`
	}{
		Status:   "promoted",
		TaskID:   in.TaskID,
		Promoted: promoted,
	})
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"task_id":  in.TaskID,
			"promoted": promoted,
		},
	}, nil
}

// validateBasename 拒绝 "/" / ".." / 绝对路径 / 隐藏文件等。source 和 as
// 都必须是 basename 形式，防 LLM 写 "../../etc/passwd" 这种越界访问，
// 也防 promote 在 deliverables/ 下创建意外子目录。
func validateBasename(f string) error {
	if strings.TrimSpace(f) == "" {
		return fmt.Errorf("empty")
	}
	if strings.ContainsAny(f, "/\\") {
		return fmt.Errorf("must be a basename without path separator")
	}
	if f == "." || strings.Contains(f, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	return nil
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

const description = `把 sub-agent 的产出文件 promote 到 deliverables/，让产物变成用户可见的成品。emma 调用 —— 在收到 dispatch 返回的 task_id + outputs 后, 评估质量合理 → 调本工具拷贝到 deliverables/。

入参:
- task_id: 来自 dispatch 工具返回的 metadata.task_id (例如 "t-7f21d356-8ee")
- promotions: 要 promote 的文件列表 (一次调用支持多项), 每项是 {source, as?}
  - source: task_dir 内的源文件名 basename (例: report.md)
  - as: 可选, deliverables/ 下的目标文件名 basename; **默认保持源文件名**, 只在同名冲突时才显式指定

命名策略 (重要):
- **默认保持源文件名** —— deliverables/ 是用户视角的文件夹, 名字直接影响用户理解。"report.md" 比 "t-abc__report.md" 友好得多, 默认不要加任何前缀。
- **同名冲突时本工具会报错**, 不会自动覆盖也不会自动加前缀。你看到 "deliverables/<name> 已存在" 的错误后, 在 promotions 那一项加上 "as" 字段, 起一个**用户能看懂、有可读性后缀**的新名字。可选风格:
  - 版本号: report_v2.md / design_v3.md
  - 日期: report_2026-06-17.md
  - 用途 / 主题: q4_sales_report.md / design_oauth.md / email_for_intern.md
- **不要起没语义的名字**: file_1.md / output_a.md / copy_of_report.md / new_report.md 都是反例。

行为:
- session_id 由 framework 通过 ctx 注入, **你不需要传**。
- 一次调用支持多项 promotion; 任一失败回滚全部已拷贝项, 文件系统保持原子性。
- 用 cp 不是 mv: 源文件保留在 tasks/ 内, 重试 / 重放仍能访问。
- 重复 promote 同一文件 (同 source 同目标名) 会被拒。

调用时机:
- dispatch 返回 SyncOutcome 后, 你已读 outputs[].path / summary 觉得**质量合格**且**值得给用户看** → promote
- 质量不合格 → 再派一次让搭档修, **不要** promote 半成品
- dispatch 是纯文本回报 / 仅状态汇报 (无文件产物) → 不需要 promote

调用之后, 给用户的回复用 deliverables/<file> 路径 (不带技术前缀), 而不是 tasks/ 的内部路径。`
