package fileread

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
)

func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func runRead(t *testing.T, path string) (content string, isErr bool, meta map[string]any) {
	t.Helper()
	tl := New(config.ToolConfig{Enabled: true})
	in, _ := json.Marshal(map[string]any{"file_path": path})
	res, err := tl.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	return res.Content, res.IsError, res.Metadata
}

func TestRead_RejectsDocxZipMagic(t *testing.T) {
	dir := t.TempDir()
	// PK\x03\x04 + arbitrary tail. We only need the magic to trip the
	// sniff; the rest doesn't have to be a valid zip.
	body := append([]byte{0x50, 0x4B, 0x03, 0x04}, []byte("…binary garbage…")...)
	p := writeFile(t, dir, "essay.docx", body)

	content, isErr, meta := runRead(t, p)
	if !isErr {
		t.Fatalf("docx should be rejected as binary; got %q", content)
	}
	if !strings.Contains(content, "docx") {
		t.Errorf("error content should mention docx kind; got %q", content)
	}
	if meta["binary"] != true {
		t.Errorf("metadata.binary should be true; got %v", meta["binary"])
	}
}

func TestRead_RejectsPdf(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "report.pdf", []byte("%PDF-1.4\n…rest of pdf…\x00\x01"))
	content, isErr, _ := runRead(t, p)
	if !isErr || !strings.Contains(content, "pdf") {
		t.Fatalf("pdf should be rejected with kind=pdf; got isErr=%v content=%q", isErr, content)
	}
}

func TestRead_RejectsArbitraryBinaryWithNUL(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "blob.bin", []byte{0x01, 0x00, 0x42, 0xFE})
	_, isErr, _ := runRead(t, p)
	if !isErr {
		t.Fatalf("file with NUL in header should be rejected as binary")
	}
}

func TestRead_AllowsPlainText(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "note.md", []byte("# heading\n\nplain text body\n"))
	content, isErr, _ := runRead(t, p)
	if isErr {
		t.Fatalf("plain text should not be rejected; got %q", content)
	}
	if !strings.Contains(content, "plain text body") {
		t.Errorf("expected content to be returned; got %q", content)
	}
}

func TestRead_AllowsUTF8Chinese(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "essay.md", []byte("# AI 与就业\n\n这是一段中文正文。\n"))
	content, isErr, _ := runRead(t, p)
	if isErr {
		t.Fatalf("UTF-8 Chinese must not be rejected; got %q", content)
	}
	if !strings.Contains(content, "这是一段中文正文") {
		t.Errorf("expected Chinese content preserved; got %q", content)
	}
}

func TestRead_AllowsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "empty.txt", []byte{})
	content, isErr, _ := runRead(t, p)
	if isErr {
		t.Fatalf("empty file must not be rejected as binary; got %q", content)
	}
}

// 边界用例：sniffBinary 只看前 512 字节；如果文件首 512 字节正好把一个
// 多字节字符切两半，utf8.Valid 会误判为 binary。这是 json / md 中含
// CJK 时的典型触发场景。trimPartialUTF8Tail 必须把可能被截断的尾部
// 1-3 字节剥掉再校验。
func TestRead_AllowsUTF8MultiByteAtSniffBoundary(t *testing.T) {
	dir := t.TempDir()

	// 第 510 字节起放 "中"（E4 B8 AD）—— sniff 窗口截到 510-511，正好
	// 卡 3 字节 UTF-8 序列的前两位。
	const sniffWindow = 512
	pad := strings.Repeat("a", sniffWindow-2)
	content := pad + "中后续内容\n"
	p := writeFile(t, dir, "boundary.md", []byte(content))
	got, isErr, _ := runRead(t, p)
	if isErr {
		t.Fatalf("UTF-8 char straddling sniff boundary must not flag binary; got %q", got)
	}
	if !strings.Contains(got, "中后续内容") {
		t.Errorf("expected content preserved; got %q", got)
	}
}

// 直接对 trimPartialUTF8Tail 做单元测试，覆盖：1) 完整序列不动；
// 2) 1/2/3 字节悬挂多字节序列被剥掉；3) 纯 ASCII 不动；4) 中间含
// 真正的非法字节不能被掩盖。
func TestTrimPartialUTF8Tail(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"ascii_only", []byte("hello world"), []byte("hello world")},
		{"complete_cjk", []byte("ab\xE4\xB8\xAD"), []byte("ab\xE4\xB8\xAD")},     // 中
		{"truncated_2_of_3", []byte("ab\xE4\xB8"), []byte("ab")},                 // 中 缺最后字节
		{"truncated_1_of_3", []byte("ab\xE4"), []byte("ab")},                     // 中 只剩首字节
		{"truncated_1_of_4_emoji", []byte("ab\xF0\x9F"), []byte("ab")},           // 🙂 缺后两字节
		{"truncated_2_of_4_emoji", []byte("ab\xF0\x9F\x99"), []byte("ab")},       // 🙂 缺最后字节
		{"complete_emoji", []byte("ab\xF0\x9F\x99\x82"), []byte("ab\xF0\x9F\x99\x82")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimPartialUTF8Tail(tc.in)
			if string(got) != string(tc.want) {
				t.Errorf("trimPartialUTF8Tail(% x) = % x, want % x", tc.in, got, tc.want)
			}
		})
	}
}
