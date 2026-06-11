// Package fileread implements the FileRead (Read) tool for reading file contents.
package fileread

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const toolName = "read"

// readInput is the JSON structure the LLM sends to invoke the tool.
type readInput struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset,omitempty"` // 1-based line number to start from
	Limit    *int   `json:"limit,omitempty"`  // number of lines to read
}

// FileReadTool reads files and returns their contents with line numbers.
type FileReadTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a FileReadTool with the given config.
func New(cfg config.ToolConfig) *FileReadTool {
	return &FileReadTool{cfg: cfg}
}

func (t *FileReadTool) Name() string                   { return toolName }
func (t *FileReadTool) Description() string            { return fileReadDescription }
func (t *FileReadTool) IsReadOnly() bool                   { return true }
func (t *FileReadTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *FileReadTool) IsConcurrencySafe() bool        { return true }
func (t *FileReadTool) IsEnabled() bool                { return t.cfg.Enabled }

func (t *FileReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "要读取文件的绝对路径。",
			},
			"offset": map[string]any{
				"type":        "number",
				"description": "起始行号。仅在文件过大、需要读指定区段时使用。",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "要读取的行数。仅在文件过大、需要读指定区段时使用。",
			},
		},
		"required": []string{"file_path"},
	}
}

func (t *FileReadTool) ValidateInput(input json.RawMessage) error {
	var ri readInput
	if err := json.Unmarshal(input, &ri); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if ri.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	return nil
}

func (t *FileReadTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var ri readInput
	if err := json.Unmarshal(input, &ri); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}

	if res := tool.EnforceReadScope(ctx, ri.FilePath); res != nil {
		return res, nil
	}

	// Check file exists.
	info, err := os.Stat(ri.FilePath)
	if err != nil {
		return &types.ToolResult{Content: fmt.Sprintf("file not found: %s", ri.FilePath), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if info.IsDir() {
		return &types.ToolResult{Content: fmt.Sprintf("%s is a directory, not a file. Use ls via Bash to list directory contents.", ri.FilePath), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}

	// Default limits.
	offset := 1
	limit := 2000
	if ri.Offset != nil && *ri.Offset > 0 {
		offset = *ri.Offset
	}
	if ri.Limit != nil && *ri.Limit > 0 {
		limit = *ri.Limit
	}

	// Binary sniff. Without this read happily returns the raw bytes of
	// docx / pdf / zip / image files, which the LLM then either fills its
	// context with garbled mojibake or attempts to "decode" by guessing
	// at a shell pipeline (`file …`, `unzip -l …`). Observed: an L2
	// scheduler tried to inspect AI生活.docx with read, got 41 KB of
	// binary noise, then issued `bash file …` which failed because bash
	// wasn't in its tool palette — the bash error was a symptom, the
	// root cause was that read shouldn't have returned bytes for a .docx
	// at all.
	if kind, ok := sniffBinary(ri.FilePath, info.Size()); ok {
		return &types.ToolResult{
			Content: fmt.Sprintf(
				"binary file (%s, %d bytes) — read returns text only. "+
					"For docx/pdf use a format-aware skill (e.g. docx skill's "+
					"scripts); for raw inspection use glob/ls; do not try to "+
					"shell out a generic decoder.",
				kind, info.Size(),
			),
			IsError:   true,
			ErrorType: types.ToolErrorInvalidInput,
			Metadata: map[string]any{
				"file_path": ri.FilePath,
				"binary":    true,
				"kind":      kind,
				"size":      info.Size(),
			},
		}, nil
	}

	// Read file with line numbers (cat -n format).
	f, err := os.Open(ri.FilePath)
	if err != nil {
		return &types.ToolResult{Content: "error opening file: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}
	defer f.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	lineNum := 0
	linesRead := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < offset {
			continue
		}
		if linesRead >= limit {
			break
		}
		sb.WriteString(fmt.Sprintf("%6d\t%s\n", lineNum, scanner.Text()))
		linesRead++
	}

	if err := scanner.Err(); err != nil {
		return &types.ToolResult{Content: "error reading file: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	if sb.Len() == 0 {
		return &types.ToolResult{
			Content: "(empty file)",
			Metadata: map[string]any{
				"render_hint": "code",
				"file_path":   ri.FilePath,
				"language":    tool.ExtToLanguage(filepath.Ext(ri.FilePath)),
			},
		}, nil
	}

	return &types.ToolResult{
		Content: sb.String(),
		Metadata: map[string]any{
			"render_hint": "code",
			"file_path":   ri.FilePath,
			"language":    tool.ExtToLanguage(filepath.Ext(ri.FilePath)),
			"start_line":  offset,
			"lines_read":  linesRead,
		},
	}, nil
}

// sniffBinary peeks the first 512 bytes of path to decide whether it is
// a binary file the LLM should not see as text. Returns (kind, true) for
// recognized binary formats — magic-byte hits first (most certain), then
// a UTF-8 validity fallback for anything else with non-text bytes.
// Returns ("", false) for plain text, empty files, or unreadable paths
// (the caller will hit the same error path again via os.Open and return
// a proper error).
//
// Heuristic only: a perverse UTF-8 file starting with PK is misclassified
// as zip. Acceptable — the alternative (returning 30 KB of zip bytes
// inline) is strictly worse.
func sniffBinary(path string, size int64) (string, bool) {
	if size == 0 {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "", false
	}
	head := buf[:n]

	switch {
	case bytes.HasPrefix(head, []byte{0x50, 0x4B, 0x03, 0x04}),
		bytes.HasPrefix(head, []byte{0x50, 0x4B, 0x05, 0x06}),
		bytes.HasPrefix(head, []byte{0x50, 0x4B, 0x07, 0x08}):
		// PK\x03\x04 is the local-file header for any zip family — docx /
		// xlsx / pptx / jar / apk / odt all share it. Disambiguate by
		// extension since the LLM cares about the document type, not
		// the container.
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".docx":
			return "docx (zip)", true
		case ".xlsx":
			return "xlsx (zip)", true
		case ".pptx":
			return "pptx (zip)", true
		case ".odt", ".ods", ".odp":
			return "opendocument (zip)", true
		case ".jar":
			return "jar (zip)", true
		case ".apk":
			return "apk (zip)", true
		case ".epub":
			return "epub (zip)", true
		}
		return "zip archive", true
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf", true
	case bytes.HasPrefix(head, []byte{0x7F, 0x45, 0x4C, 0x46}):
		return "ELF executable", true
	case bytes.HasPrefix(head, []byte{0x4D, 0x5A}):
		return "PE executable", true
	case bytes.HasPrefix(head, []byte{0xCA, 0xFE, 0xBA, 0xBE}):
		return "java class", true
	case bytes.HasPrefix(head, []byte{0x89, 0x50, 0x4E, 0x47}):
		return "png image", true
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg image", true
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "gif image", true
	case bytes.HasPrefix(head, []byte("RIFF")) && n >= 12 && bytes.Equal(head[8:12], []byte("WEBP")):
		return "webp image", true
	case bytes.HasPrefix(head, []byte{0x1F, 0x8B}):
		return "gzip", true
	case bytes.HasPrefix(head, []byte("BZh")):
		return "bzip2", true
	case bytes.HasPrefix(head, []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}):
		return "xz", true
	case bytes.HasPrefix(head, []byte("SQLite format 3\x00")):
		return "sqlite database", true
	case bytes.HasPrefix(head, []byte("ID3")),
		bytes.HasPrefix(head, []byte{0xFF, 0xFB}),
		bytes.HasPrefix(head, []byte{0xFF, 0xF3}):
		return "mp3 audio", true
	}

	// Fallback heuristic: any NUL byte in the prefix or invalid UTF-8 over
	// the whole sniff window means "not text". Skip files that look like
	// short ASCII (under 4 bytes) — those don't carry enough signal to
	// classify and are almost certainly text fragments.
	if bytes.IndexByte(head, 0) >= 0 {
		return "binary (NUL in header)", true
	}
	if n >= 4 && !utf8.Valid(trimPartialUTF8Tail(head)) {
		return "binary (invalid utf-8)", true
	}
	return "", false
}

// trimPartialUTF8Tail strips up to 3 trailing bytes that look like an
// incomplete UTF-8 multi-byte sequence — happens when a CJK/emoji/accented
// character straddles the fixed-size sniff window boundary. Without this,
// e.g. a JSON or Markdown file ending its first 512 bytes mid-character
// is falsely flagged as binary.
func trimPartialUTF8Tail(b []byte) []byte {
	end := len(b)
	// Walk back over at most 3 continuation bytes (0b10xxxxxx).
	for back := 0; back < 3 && end > 0; back++ {
		if (b[end-1] & 0xC0) != 0x80 {
			break
		}
		end--
	}
	// If the byte before the peeled region is a multi-byte start byte
	// (high bit set) but its full sequence wasn't present in the buffer,
	// drop it too. Otherwise the trailing chars formed a complete rune
	// and we restore them.
	if end > 0 && (b[end-1]&0x80) != 0 {
		if utf8.FullRune(b[end-1:]) {
			return b
		}
		end--
	}
	return b[:end]
}

const fileReadDescription = `读取本地文件系统中的文件。可以直接访问任意文件。

使用规范：
- file_path 必须是绝对路径，不能相对路径。
- 默认从文件开头读最多 2000 行。
- 文件较大时用 offset 和 limit 读指定区段。
- 返回结果按 cat -n 风格，行号从 1 开始。`
