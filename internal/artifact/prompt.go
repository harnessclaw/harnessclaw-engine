package artifact

import (
	"fmt"
	"strings"
)

// DefaultPreambleMaxItems caps the number of artifacts listed in the
// preamble. Doc §6.B warns that auto-injection becomes counterproductive
// when too many artifacts pile up — the L3 starts spending tokens reading
// metadata it doesn't need. 10 is empirically generous for one trace;
// callers that need more should switch to ArtifactSearch (planned).
const DefaultPreambleMaxItems = 10

// DefaultPreamblePreviewBytes caps the preview length per item embedded in
// the preamble. Stored artifacts may have a much longer preview field
// (DefaultPreviewBytes = 512); 160 bytes here is enough to recognise a
// document and decide whether to read it, without bloating the L3's
// task prompt.
const DefaultPreamblePreviewBytes = 160

// RenderAvailableList builds the "<available-artifacts>" block that gets
// prepended to an L3 task prompt. Returns "" when arts is empty so callers
// can compose unconditionally without an extra nil check.
//
// Doc §6 mode A: instead of L2 pasting content into L3's prompt, L2 (or
// the framework on L2's behalf) lists artifact_ids and lets L3 decide
// what to read. This function renders that list in a stable, parseable
// shape.
//
// The XML-tag wrapper is intentional: it gives the LLM a clear boundary
// between "context I was handed" and "the actual task", which is
// historically more reliable than markdown headers when prompts get long.
func RenderAvailableList(arts []*Artifact, maxItems int) string {
	if len(arts) == 0 {
		return ""
	}
	if maxItems <= 0 {
		maxItems = DefaultPreambleMaxItems
	}

	var b strings.Builder
	b.WriteString("<available-artifacts>\n")
	b.WriteString("以下 artifact 已在当前 trace 中可读，按需用 ArtifactRead 取用——默认 mode=preview，足够再升级 full。\n\n")

	shown := arts
	truncated := false
	if len(shown) > maxItems {
		shown = shown[:maxItems]
		truncated = true
	}

	for _, a := range shown {
		writeArtifactLine(&b, a)
	}

	if truncated {
		fmt.Fprintf(&b, "\n（共 %d 条，仅显示前 %d 条；如需完整列表用 ArtifactList。）\n", len(arts), maxItems)
	}
	b.WriteString("</available-artifacts>")
	return b.String()
}

// writeArtifactLine emits one entry. Format:
//
//	- art_xxx (file, 12.4KB) — sales-2024.md: 2024 中国新能源车销量
//	  preview: 2024 年中国新能源车销量约 1100 万辆…
//
// Producer/version are intentionally omitted from the LLM-facing line —
// they help with debugging but rarely change the read decision. We can
// surface them on demand later if a worker reports needing them.
func writeArtifactLine(b *strings.Builder, a *Artifact) {
	fmt.Fprintf(b, "- %s (%s, %s)", a.ID, a.Type, HumanSize(a.Size))
	switch {
	case a.Name != "" && a.Description != "":
		fmt.Fprintf(b, " — %s: %s", a.Name, a.Description)
	case a.Name != "":
		fmt.Fprintf(b, " — %s", a.Name)
	case a.Description != "":
		fmt.Fprintf(b, " — %s", a.Description)
	}
	b.WriteString("\n")
	if p := truncatePreview(a.Preview, DefaultPreamblePreviewBytes); p != "" {
		fmt.Fprintf(b, "  preview: %s\n", p)
	}
}

// truncatePreview is a UTF-8-safe shortener that adds an ellipsis when it
// actually cuts. Newlines inside the preview are collapsed to spaces so a
// stray "\n" doesn't break the per-line layout.
func truncatePreview(s string, maxBytes int) string {
	if s == "" {
		return ""
	}
	collapsed := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	collapsed = strings.TrimSpace(collapsed)
	if collapsed == "" {
		return ""
	}
	cut := MakePreview(collapsed, maxBytes)
	return cut
}

// HumanSize formats bytes as "12.4KB" / "1.2MB". Kept tiny because the
// preamble shows it in-line and we don't want a 5-significant-figure
// number stealing attention from the description. Exported so the
// engine layer can reuse it when rendering artifact lists into
// SpawnResult.Output (so emma sees the same compact size format).
func HumanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// WrapTaskWithPreamble returns the given task prompt wrapped with an
// available-artifacts preamble. Callers can hand the result to message
// construction directly. Returns task unchanged when no artifacts.
//
// We wrap the original task in a <task> tag so the LLM never confuses
// the framework-injected preamble with what the parent actually asked
// for. Doc §6 mode A pattern.
func WrapTaskWithPreamble(task string, arts []*Artifact, maxItems int) string {
	preamble := RenderAvailableList(arts, maxItems)
	if preamble == "" {
		return task
	}
	return preamble + "\n\n<task>\n" + task + "\n</task>"
}
