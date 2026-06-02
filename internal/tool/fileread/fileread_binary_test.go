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
