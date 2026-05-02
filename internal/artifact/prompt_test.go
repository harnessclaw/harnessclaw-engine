package artifact

import (
	"strings"
	"testing"
	"time"
)

func mkArt(id, name, desc string, size int, preview string) *Artifact {
	return &Artifact{
		ID:          id,
		Type:        TypeFile,
		Name:        name,
		Description: desc,
		Size:        size,
		Preview:     preview,
		CreatedAt:   time.Now().UTC(),
	}
}

func TestRenderAvailableList_EmptyReturnsEmpty(t *testing.T) {
	// Callers compose unconditionally; an empty slice must never produce
	// a stray "<available-artifacts></available-artifacts>" wrapper.
	if got := RenderAvailableList(nil, 0); got != "" {
		t.Errorf("nil → want empty, got %q", got)
	}
	if got := RenderAvailableList([]*Artifact{}, 0); got != "" {
		t.Errorf("[]*Artifact{} → want empty, got %q", got)
	}
}

func TestRenderAvailableList_IncludesIDAndDescription(t *testing.T) {
	// Doc §6 mode A: L3 reads the list, decides what to fetch. The ID is
	// non-negotiable (it's the only way to call ArtifactRead) and the
	// description is the load-bearing decision aid.
	arts := []*Artifact{mkArt("art_aaa", "report.md", "2024 销量", 12450, "2024 销量约 1100 万")}
	out := RenderAvailableList(arts, 0)

	for _, want := range []string{
		"<available-artifacts>",
		"</available-artifacts>",
		"art_aaa",
		"report.md",
		"2024 销量",
		"preview:",
		"ArtifactRead",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preamble missing %q\nfull text:\n%s", want, out)
		}
	}
}

func TestRenderAvailableList_TruncatesAtMaxItems(t *testing.T) {
	// Doc §6.B warning: too many artifacts → context bloat. We cap at
	// maxItems and tell the LLM how to fetch the rest (ArtifactList).
	arts := make([]*Artifact, 15)
	for i := range arts {
		arts[i] = mkArt("art_"+string(rune('a'+i))+string(rune('a'+i)), "f", "d", 100, "p")
	}
	out := RenderAvailableList(arts, 5)

	count := strings.Count(out, "art_")
	if count != 5 {
		t.Errorf("expected 5 listed (saw %d hits)\n%s", count, out)
	}
	if !strings.Contains(out, "共 15") {
		t.Errorf("truncation marker should report total; got:\n%s", out)
	}
	if !strings.Contains(out, "ArtifactList") {
		t.Errorf("truncation marker should hint at ArtifactList; got:\n%s", out)
	}
}

func TestRenderAvailableList_PreviewIsCollapsedAndTruncated(t *testing.T) {
	// A preview field can carry raw newlines (e.g. multi-line markdown).
	// Embedding that verbatim breaks the per-line list layout and confuses
	// the LLM about block boundaries. Collapse newlines to spaces.
	long := strings.Repeat("中文段落预览。", 200) // 1.4KB+ of multibyte
	arts := []*Artifact{mkArt("art_bbb", "x", "x", 100, "line1\nline2\n\nline3 "+long)}
	out := RenderAvailableList(arts, 0)

	preview := extractPreview(out)
	if strings.Contains(preview, "\n") {
		t.Errorf("preview leaked newlines: %q", preview)
	}
	if len(preview) > DefaultPreamblePreviewBytes+8 /* allow ellipsis padding */ {
		t.Errorf("preview not truncated: len=%d cap=%d", len(preview), DefaultPreamblePreviewBytes)
	}
}

func TestRenderAvailableList_HumanSize(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{500, "500B"},
		{12450, "12.2KB"},
		{2 * 1024 * 1024, "2.0MB"},
	}
	for _, c := range cases {
		out := RenderAvailableList([]*Artifact{mkArt("art_"+c.want, "x", "y", c.n, "")}, 0)
		if !strings.Contains(out, c.want) {
			t.Errorf("size %d → want %q in output, got:\n%s", c.n, c.want, out)
		}
	}
}

func TestWrapTaskWithPreamble_NoArtifacts(t *testing.T) {
	// When the parent has nothing to share, the task prompt must come
	// through verbatim — wrapping it in tags would change the LLM's
	// interpretation for no benefit.
	got := WrapTaskWithPreamble("写个邮件", nil, 0)
	if got != "写个邮件" {
		t.Errorf("no-artifacts path should pass through; got %q", got)
	}
}

func TestWrapTaskWithPreamble_WithArtifacts(t *testing.T) {
	arts := []*Artifact{mkArt("art_ccc", "data.csv", "销量数据", 5000, "month,sales")}
	got := WrapTaskWithPreamble("对比 2024 与 2023 销量", arts, 0)

	// Order matters — preamble comes first so the LLM treats the task as
	// the foreground question, the available artifacts as background.
	pAt := strings.Index(got, "<available-artifacts>")
	tAt := strings.Index(got, "<task>")
	if pAt < 0 || tAt < 0 {
		t.Fatalf("missing wrappers:\n%s", got)
	}
	if pAt > tAt {
		t.Errorf("<available-artifacts> must precede <task>; preamble@%d, task@%d", pAt, tAt)
	}
	if !strings.Contains(got, "对比 2024 与 2023 销量") {
		t.Errorf("original task body lost in wrap")
	}
	if !strings.Contains(got, "art_ccc") {
		t.Errorf("artifact ID not surfaced")
	}
}

// extractPreview pulls the value after "preview: " up to the next newline,
// trimmed. Used by the preview-truncation test.
func extractPreview(s string) string {
	const tag = "preview: "
	i := strings.Index(s, tag)
	if i < 0 {
		return ""
	}
	body := s[i+len(tag):]
	if j := strings.Index(body, "\n"); j >= 0 {
		body = body[:j]
	}
	return strings.TrimSpace(body)
}
