package workspace

import (
	"fmt"
	"strings"
)

// TaskInputRef is one upstream input shown to an L3 in its <task-inputs>
// preamble. Path is relative to sessionRoot so it shows up identically in
// the agent's view and the user's file tree.
type TaskInputRef struct {
	Path    string
	Summary string
	Bytes   int
}

// RenderTaskInputs returns the framework-injected preamble block. Returns
// "" when inputs is empty so callers can compose unconditionally.
//
// XML-tagged shape (<task-inputs>...) is intentional: it gives the LLM a
// clear boundary between framework-supplied context and the actual task,
// which is historically more reliable than markdown headers when prompts
// get long.
func RenderTaskInputs(inputs []TaskInputRef) string {
	if len(inputs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<task-inputs>\n")
	b.WriteString("以下文件路径是上游 task 产出，可按需用 FileRead 读取。默认仅读 summary 判断是否需要全文。\n\n")
	for _, in := range inputs {
		fmt.Fprintf(&b, "- %s", in.Path)
		if in.Bytes > 0 {
			fmt.Fprintf(&b, " (%s)", humanSize(in.Bytes))
		}
		if in.Summary != "" {
			fmt.Fprintf(&b, " — %s", in.Summary)
		}
		b.WriteString("\n")
	}
	b.WriteString("</task-inputs>")
	return b.String()
}

// WrapTaskWithInputs prepends the <task-inputs> block to the L3's task
// prompt, with the original task wrapped in <task> so the LLM never
// confuses framework context with the actual ask. Empty inputs yield the
// original task unchanged so callers don't need an empty-check at the
// call site.
func WrapTaskWithInputs(task string, inputs []TaskInputRef) string {
	preamble := RenderTaskInputs(inputs)
	if preamble == "" {
		return task
	}
	return preamble + "\n\n<task>\n" + task + "\n</task>"
}

// humanSize formats a byte count for the preamble. Kept tight (1 decimal
// place) because preamble lines compete with the task body for attention.
func humanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
