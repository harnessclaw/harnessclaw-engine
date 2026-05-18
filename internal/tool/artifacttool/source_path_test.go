package artifacttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// writeTestFile creates a file under dir with the given content and
// returns its absolute path. EvalSymlinks-resolved.
func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func sourcePathTestCtx(t *testing.T) (artifact.Store, context.Context) {
	t.Helper()
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	ctx := newTestCtx(store, tool.ArtifactProducer{
		AgentID:   "agent_test",
		SessionID: "sess_1",
		TraceID:   "tr_1",
	})
	return store, ctx
}

// --- happy paths ------------------------------------------------------

func TestSourcePath_BlobFileIsBase64Encoded(t *testing.T) {
	allowed := t.TempDir()
	// 写一个非 ASCII 字节的"伪 docx"，确保 base64 真在工作
	bin := []byte{0x50, 0x4B, 0x03, 0x04, 0xFF, 0x00, 0xAB, 0xCD}
	path := filepath.Join(allowed, "test.docx")
	if err := os.WriteFile(path, bin, 0o644); err != nil {
		t.Fatal(err)
	}
	wt := NewWriteTool(allowed)
	store, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{
		"type":"blob",
		"mime_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"source_path":%q
	}`, path))

	if err := wt.ValidateInput(in); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	res, err := wt.Execute(ctx, in)
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isErr=%v content=%s", err, res.IsError, res.Content)
	}

	// 验证 artifact 存的是 base64 of 原始字节
	id := getIDFromResult(t, res)
	a, _ := store.Get(context.Background(), id)
	want := base64.StdEncoding.EncodeToString(bin)
	if a.Content != want {
		t.Errorf("artifact.Content mismatch.\n  got:  %s\n  want: %s", a.Content, want)
	}
	if a.Encoding != "base64" {
		t.Errorf("encoding = %q, want base64", a.Encoding)
	}
	// 而且解码后的字节 == 原始字节（这是 docx 不损坏的关键测试）
	decoded, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil {
		t.Fatalf("decode artifact content: %v", err)
	}
	if string(decoded) != string(bin) {
		t.Errorf("round-trip bytes differ — base64 fidelity broken")
	}
}

func TestSourcePath_FileTypeStoresRawString(t *testing.T) {
	allowed := t.TempDir()
	path := writeTestFile(t, allowed, "readme.md", "# Hello\nworld")
	wt := NewWriteTool(allowed)
	store, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{
		"type":"file",
		"source_path":%q
	}`, path))

	if err := wt.ValidateInput(in); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	res, err := wt.Execute(ctx, in)
	if err != nil || res.IsError {
		t.Fatalf("Execute failed: %s", res.Content)
	}
	a, _ := store.Get(context.Background(), getIDFromResult(t, res))
	if a.Content != "# Hello\nworld" {
		t.Errorf("file type should be raw, got %q", a.Content)
	}
	if a.Encoding == "base64" {
		t.Errorf("file type should not be base64-encoded")
	}
}

func TestSourcePath_NameDefaultsToBasename(t *testing.T) {
	allowed := t.TempDir()
	path := writeTestFile(t, allowed, "report.pdf", "data")
	wt := NewWriteTool(allowed)
	store, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"blob","source_path":%q}`, path))
	res, _ := wt.Execute(ctx, in)
	if res.IsError {
		t.Fatalf("execute: %s", res.Content)
	}
	a, _ := store.Get(context.Background(), getIDFromResult(t, res))
	if a.Name != "report.pdf" {
		t.Errorf("name = %q, want report.pdf", a.Name)
	}
}

func TestSourcePath_MIMEDefaultsFromExtension(t *testing.T) {
	allowed := t.TempDir()
	path := writeTestFile(t, allowed, "data.json", `{"k":1}`)
	wt := NewWriteTool(allowed)
	store, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, path))
	res, _ := wt.Execute(ctx, in)
	if res.IsError {
		t.Fatalf("execute: %s", res.Content)
	}
	a, _ := store.Get(context.Background(), getIDFromResult(t, res))
	if !strings.HasPrefix(a.MIMEType, "application/json") {
		t.Errorf("mime = %q, want application/json", a.MIMEType)
	}
}

// --- mutual exclusion + missing-input -----------------------------------

func TestSourcePath_RejectsBothContentAndSourcePath(t *testing.T) {
	allowed := t.TempDir()
	path := writeTestFile(t, allowed, "x", "x")
	wt := NewWriteTool(allowed)

	in := json.RawMessage(fmt.Sprintf(`{"type":"file","content":"a","source_path":%q}`, path))
	err := wt.ValidateInput(in)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got %v", err)
	}
}

func TestSourcePath_RejectsNeitherContentNorSourcePath(t *testing.T) {
	wt := NewWriteTool(t.TempDir())
	in := json.RawMessage(`{"type":"file"}`)
	err := wt.ValidateInput(in)
	if err == nil || !strings.Contains(err.Error(), "either content or source_path") {
		t.Errorf("expected 'either content or source_path' error, got %v", err)
	}
}

// --- security: path validation -------------------------------------------

func TestSourcePath_RejectsRelativePath(t *testing.T) {
	wt := NewWriteTool(t.TempDir())
	_, ctx := sourcePathTestCtx(t)
	in := json.RawMessage(`{"type":"file","source_path":"./relative.txt"}`)
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "must be absolute") {
		t.Errorf("relative path should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsPathOutsideAllowlist(t *testing.T) {
	allowed := t.TempDir()
	// 创建 *不在* allowed 下的文件
	outside := t.TempDir() // 另一个独立 tmpdir
	outsidePath := writeTestFile(t, outside, "secret.txt", "leak")
	wt := NewWriteTool(allowed) // 只允许 allowed
	_, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, outsidePath))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "outside the allowed") {
		t.Errorf("outside path should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsParentTraversal(t *testing.T) {
	allowed := t.TempDir()
	parent := filepath.Dir(allowed)
	// 写一个文件到 parent 目录
	bad := filepath.Join(parent, "leak.txt")
	if err := os.WriteFile(bad, []byte("leak"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(bad)

	wt := NewWriteTool(allowed)
	_, ctx := sourcePathTestCtx(t)

	// 用 ../ 试图穿越
	traversal := filepath.Join(allowed, "..", "leak.txt")
	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, traversal))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "outside the allowed") {
		t.Errorf("../ traversal should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsSymlinkEscape(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	target := writeTestFile(t, outside, "secret.txt", "leak")

	// 在 allowed 里建一个软链接指向 outside 的文件
	link := filepath.Join(allowed, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	wt := NewWriteTool(allowed)
	_, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, link))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "outside the allowed") {
		t.Errorf("symlink escape should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsMissingFile(t *testing.T) {
	allowed := t.TempDir()
	wt := NewWriteTool(allowed)
	_, ctx := sourcePathTestCtx(t)

	path := filepath.Join(allowed, "does-not-exist.txt")
	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, path))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "does not exist") {
		t.Errorf("missing file should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsDirectory(t *testing.T) {
	allowed := t.TempDir()
	wt := NewWriteTool(allowed)
	_, ctx := sourcePathTestCtx(t)

	// allowed 自己是一个目录
	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, allowed))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "not a regular file") {
		t.Errorf("directory should be rejected, got: %s", res.Content)
	}
}

func TestSourcePath_RejectsWhenAllowlistEmpty(t *testing.T) {
	allowed := t.TempDir()
	path := writeTestFile(t, allowed, "f.txt", "x")
	wt := NewWriteTool() // <-- empty allowlist
	_, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"file","source_path":%q}`, path))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "not configured") {
		t.Errorf("empty allowlist should reject with 'not configured', got: %s", res.Content)
	}
}

// --- size guard ----------------------------------------------------------

func TestSourcePath_RejectsOversizeFile(t *testing.T) {
	allowed := t.TempDir()
	// 写一个 60 MB 的文件（超 50 MB 限制）
	big := filepath.Join(allowed, "huge.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(60 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	wt := NewWriteTool(allowed)
	_, ctx := sourcePathTestCtx(t)

	in := json.RawMessage(fmt.Sprintf(`{"type":"blob","source_path":%q}`, big))
	res, _ := wt.Execute(ctx, in)
	if !res.IsError || !strings.Contains(res.Content, "exceeds") {
		t.Errorf("oversize file should be rejected, got: %s", res.Content)
	}
}

// --- pathHasAnyPrefix unit -----------------------------------------------

func TestPathHasAnyPrefix(t *testing.T) {
	cases := []struct {
		cand    string
		allowed []string
		want    bool
		name    string
	}{
		{"/home/x/workspace/file", []string{"/home/x/workspace"}, true, "child"},
		{"/home/x/workspace", []string{"/home/x/workspace"}, true, "same"},
		{"/home/x/workspace_evil", []string{"/home/x/workspace"}, false, "prefix-confusion-prevention"},
		{"/home/x/other", []string{"/home/x/workspace"}, false, "unrelated"},
		{"/home/x/file", []string{"/home/x/workspace", "/home/x/skills"}, false, "outside all"},
		{"/home/x/skills/foo", []string{"/home/x/workspace", "/home/x/skills"}, true, "second allowed"},
	}
	for _, c := range cases {
		got := pathHasAnyPrefix(c.cand, c.allowed)
		if got != c.want {
			t.Errorf("%s: cand=%q allowed=%v → %v, want %v", c.name, c.cand, c.allowed, got, c.want)
		}
	}
}

// --- helpers --------------------------------------------------------------

// getIDFromResult parses a ToolResult.Content (JSON) and returns artifact_id.
func getIDFromResult(t *testing.T, res *types.ToolResult) string {
	t.Helper()
	var out struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if out.ArtifactID == "" {
		t.Fatalf("empty artifact_id")
	}
	return out.ArtifactID
}
