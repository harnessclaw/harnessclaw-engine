package toolphrase

import (
	"fmt"
	"strings"

	emitv2 "harnessclaw-go/internal/channel/emit/v2"
)

// Phrase bundles a category × phase key with its candidate templates.
// Templates support these placeholders:
//   - {bytes}        — humanized byte count (e.g. "1.2KB")
//   - {attempt}      — retry attempt index (1-based)
//   - {max}          — max retry attempts
//   - {seconds}      — retry delay in whole seconds
type Phrase struct {
	Category  ToolCategory
	Phase     emitv2.ToolPhase
	Templates []string
}

// RetryInfo is passed to interpolate when filling retry-specific
// templates. Optional; nil = no retry context.
type RetryInfo struct {
	Attempt      int
	Max          int
	DelaySeconds int
}

// library is the master table. Order doesn't matter — lookup is by
// (Category, Phase) key. Adding new (category, phase) entries here is
// the only way to introduce new copy.
var library = []Phrase{
	// T1 PLANNING
	{CategoryWrite, emitv2.PhasePlanning, []string{
		"构思文件内容", "草拟内容中", "准备落笔", "整理要写入的结构",
	}},
	{CategoryExec, emitv2.PhasePlanning, []string{
		"梳理执行思路", "拼接命令中", "构造调用语句", "组织参数",
	}},
	{CategoryRead, emitv2.PhasePlanning, []string{
		"确认查询位置", "整理查询参数", "锁定目标",
	}},
	{CategoryNetwork, emitv2.PhasePlanning, []string{
		"整理搜索关键词", "明确查询意图", "确定要找什么",
	}},
	{CategoryDispatch, emitv2.PhasePlanning, []string{
		"整理任务描述", "明确派发目标", "组织任务上下文",
	}},
	{CategoryGeneric, emitv2.PhasePlanning, []string{
		"正在准备工具调用", "整理调用参数",
	}},

	// T2 PLANNING_ARGS
	{CategoryWrite, emitv2.PhasePlanningArgs, []string{
		"已写入 {bytes}", "生成中 · {bytes}", "草拟中 · {bytes}", "落笔中 · {bytes}",
	}},
	{CategoryExec, emitv2.PhasePlanningArgs, []string{
		"构造命令 · {bytes}", "拼接中 · {bytes}",
	}},
	{CategoryDispatch, emitv2.PhasePlanningArgs, []string{
		"拼装任务 · {bytes}", "组织上下文 · {bytes}",
	}},
	{CategoryGeneric, emitv2.PhasePlanningArgs, []string{
		"生成中 · {bytes}",
	}},

	// T3 QUEUED
	{CategoryGeneric, emitv2.PhaseQueued, []string{
		"准备执行", "已就绪", "排队待执行", "正在处理",
	}},

	// T4 PERMISSION_WAIT
	{CategoryGeneric, emitv2.PhasePermissionWait, []string{
		"等待你的确认", "需要授权才能继续", "请审批此操作", "等待权限放行",
	}},

	// T5 EXECUTING
	{CategoryWrite, emitv2.PhaseExecuting, []string{
		"写入文件中", "落盘中", "正在保存",
	}},
	{CategoryRead, emitv2.PhaseExecuting, []string{
		"读取中", "查找中", "扫描中", "搜索匹配",
	}},
	{CategoryExec, emitv2.PhaseExecuting, []string{
		"命令运行中", "Shell 执行中", "正在执行",
	}},
	{CategoryNetwork, emitv2.PhaseExecuting, []string{
		"正在搜索", "等待网络响应", "拉取数据中", "请求外部接口",
	}},
	{CategoryDispatch, emitv2.PhaseExecuting, []string{
		"子代理工作中", "派发已激活",
	}},
	{CategoryGeneric, emitv2.PhaseExecuting, []string{
		"执行中", "正在处理",
	}},

	// M4 NEXT_ROUND
	{CategoryGeneric, emitv2.PhaseNextRound, []string{
		"正在解读结果", "整合刚才的输出", "重新梳理思路", "评估返回内容", "根据结果思考下一步",
	}},
}

// lookup returns the templates for a (category, phase) pair. Falls back
// to (CategoryGeneric, phase) if the specific category has no entry.
// Returns nil if even the generic fallback is absent — caller decides
// what to do (typically returns "" and lets the front-end render a
// default based on the Phase enum).
func lookup(cat ToolCategory, phase emitv2.ToolPhase) []string {
	var generic []string
	for _, e := range library {
		if e.Phase != phase {
			continue
		}
		if e.Category == cat {
			return e.Templates
		}
		if e.Category == CategoryGeneric {
			generic = e.Templates
		}
	}
	return generic
}

// humanizeBytes formats a raw byte count for inline display. Caps at
// "1.0MB+" so very large args don't make the counter jitter with
// meaningless precision.
func humanizeBytes(n int) string {
	switch {
	case n < 0:
		return "0B"
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 2*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return "1.0MB+"
	}
}

// interpolate fills placeholder tokens in a template. Unknown tokens
// stay verbatim (so adding new placeholders is forward-compatible).
func interpolate(tmpl string, bytes int, retry *RetryInfo) string {
	out := tmpl
	if strings.Contains(out, "{bytes}") {
		out = strings.ReplaceAll(out, "{bytes}", humanizeBytes(bytes))
	}
	if retry != nil {
		if strings.Contains(out, "{attempt}") {
			out = strings.ReplaceAll(out, "{attempt}", fmt.Sprintf("%d", retry.Attempt))
		}
		if strings.Contains(out, "{max}") {
			out = strings.ReplaceAll(out, "{max}", fmt.Sprintf("%d", retry.Max))
		}
		if strings.Contains(out, "{seconds}") {
			out = strings.ReplaceAll(out, "{seconds}", fmt.Sprintf("%d", retry.DelaySeconds))
		}
	}
	return out
}
