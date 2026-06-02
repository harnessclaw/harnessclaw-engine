package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// invokeWrite is a thin helper that constructs the JSON envelope the
// real call site builds, so the size-limit test goes through the same
// Unmarshal path as a tool dispatch from loop.Run.
func invokeWrite(t *testing.T, path, content string) *types.ToolResult {
	t.Helper()
	tl := New(config.ToolConfig{Enabled: true})
	in, _ := json.Marshal(map[string]any{"file_path": path, "content": content})
	res, err := tl.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	return res
}

func TestWrite_RejectsOversizedContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.js")
	// One byte over the limit — covers the boundary cleanly.
	content := strings.Repeat("a", maxContentChars+1)

	res := invokeWrite(t, p, content)
	if !res.IsError {
		t.Fatalf("oversized content should be rejected; got success: %q", res.Content)
	}
	if res.ErrorType != types.ToolErrorInvalidInput {
		t.Errorf("ErrorType = %v, want invalid_input", res.ErrorType)
	}
	// The error must steer the LLM toward edit/bash-heredoc, not just
	// surface a number. Otherwise the LLM retries the same payload.
	for _, want := range []string{"edit", "bash", "DO NOT retry"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("error message must mention %q to steer remediation; got %q", want, res.Content)
		}
	}
	if _, err := os.Stat(p); err == nil {
		t.Errorf("file should NOT have been written on size rejection: %s", p)
	}
}

// TestInputSchema_DeclaresContentMaxLength: the size limit must reach
// the LLM via the schema (structural), not just via the description
// (free text). Anthropic/OpenAI propagate `maxLength` as a real
// constraint; the LLM is far more likely to honour it than to read
// the prose description. Without this assertion a future cleanup could
// strip maxLength and the limit becomes a silent server-side reject
// the LLM never sees coming.
func TestInputSchema_DeclaresContentMaxLength(t *testing.T) {
	tl := New(config.ToolConfig{Enabled: true})
	schema := tl.InputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	content, ok := props["content"].(map[string]any)
	if !ok {
		t.Fatal("schema should have content property")
	}
	got, ok := content["maxLength"].(int)
	if !ok {
		t.Fatalf("content.maxLength missing or wrong type: %v", content["maxLength"])
	}
	if got != maxContentChars {
		t.Errorf("content.maxLength = %d, want %d", got, maxContentChars)
	}
	desc, _ := content["description"].(string)
	wantNum := strconv.Itoa(maxContentChars)
	if !strings.Contains(desc, wantNum) {
		t.Errorf("content.description should mention the explicit char limit %q; got %q", wantNum, desc)
	}
}

func TestWrite_AcceptsAtLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "exact.txt")
	content := strings.Repeat("a", maxContentChars)

	res := invokeWrite(t, p, content)
	if res.IsError {
		t.Fatalf("exactly maxContentChars should be accepted; got error: %q", res.Content)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != maxContentChars {
		t.Errorf("written size = %d, want %d", len(got), maxContentChars)
	}
}
