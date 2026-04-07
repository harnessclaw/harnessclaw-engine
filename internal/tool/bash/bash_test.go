package bash

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/config"
)

func newTestTool() *BashTool {
	return New(config.ToolConfig{
		Enabled: true,
		Timeout: 30 * time.Second,
	})
}

func makeInput(command string) json.RawMessage {
	b, _ := json.Marshal(bashInput{Command: command})
	return b
}

func makeInputWithTimeout(command string, timeoutMs int) json.RawMessage {
	b, _ := json.Marshal(bashInput{Command: command, Timeout: &timeoutMs})
	return b
}

// --- Basic interface tests ---

func TestName(t *testing.T) {
	bt := newTestTool()
	if bt.Name() != "Bash" {
		t.Errorf("expected 'Bash', got %q", bt.Name())
	}
}

func TestIsReadOnly(t *testing.T) {
	bt := newTestTool()
	if bt.IsReadOnly() {
		t.Error("Bash tool should not be read-only")
	}
}

func TestIsEnabled(t *testing.T) {
	bt := New(config.ToolConfig{Enabled: true})
	if !bt.IsEnabled() {
		t.Error("expected enabled")
	}

	bt2 := New(config.ToolConfig{Enabled: false})
	if bt2.IsEnabled() {
		t.Error("expected disabled")
	}
}

func TestInputSchema(t *testing.T) {
	bt := newTestTool()
	schema := bt.InputSchema()

	if schema["type"] != "object" {
		t.Error("schema type should be object")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	if _, ok := props["command"]; !ok {
		t.Error("schema should have command property")
	}
	req, ok := schema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "command" {
		t.Error("command should be the only required field")
	}
}

func TestDescription(t *testing.T) {
	bt := newTestTool()
	desc := bt.Description()
	if !strings.Contains(desc, "bash command") {
		t.Error("description should mention bash command")
	}
}

// --- ValidateInput tests ---

func TestValidateInput_Valid(t *testing.T) {
	bt := newTestTool()
	if err := bt.ValidateInput(makeInput("echo hello")); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateInput_EmptyCommand(t *testing.T) {
	bt := newTestTool()
	if err := bt.ValidateInput(makeInput("")); err == nil {
		t.Error("expected error for empty command")
	}
	if err := bt.ValidateInput(makeInput("   ")); err == nil {
		t.Error("expected error for whitespace-only command")
	}
}

func TestValidateInput_InvalidJSON(t *testing.T) {
	bt := newTestTool()
	if err := bt.ValidateInput(json.RawMessage(`{broken`)); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// --- Execute tests ---

func TestExecute_SimpleCommand(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	result, err := bt.Execute(ctx, makeInput("echo hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected 'hello' in output, got %q", result.Content)
	}
}

func TestExecute_ExitCode(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	result, err := bt.Execute(ctx, makeInput("exit 42"))
	if err != nil {
		t.Fatal(err)
	}
	// Non-zero exit is not IsError (only context errors are).
	if result.IsError {
		t.Error("non-zero exit should not set IsError")
	}
	if result.Metadata["exit_code"] != 42 {
		t.Errorf("expected exit_code=42, got %v", result.Metadata["exit_code"])
	}
	if !strings.Contains(result.Content, "[exit code: 42]") {
		t.Errorf("expected exit code in content, got %q", result.Content)
	}
}

func TestExecute_Stderr(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	result, err := bt.Execute(ctx, makeInput("echo out && echo err >&2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "out") {
		t.Errorf("expected stdout in output, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "err") {
		t.Errorf("expected stderr in output, got %q", result.Content)
	}
}

func TestExecute_Timeout(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	// 500ms timeout with a 10s sleep.
	result, err := bt.Execute(ctx, makeInputWithTimeout("sleep 10", 500))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected IsError for timed-out command")
	}
	if !strings.Contains(result.Content, "timed out") {
		t.Errorf("expected timeout message, got %q", result.Content)
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	bt := newTestTool()
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	result, err := bt.Execute(ctx, makeInput("sleep 10"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected IsError for cancelled command")
	}
}

func TestExecute_CwdPersistence(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	// Change directory.
	_, err := bt.Execute(ctx, makeInput("cd /tmp"))
	if err != nil {
		t.Fatal(err)
	}

	// Next command should see the new CWD.
	result, err := bt.Execute(ctx, makeInput("pwd -P"))
	if err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(result.Content)
	// macOS resolves /tmp -> /private/tmp.
	if runtime.GOOS == "darwin" {
		if got != "/private/tmp" {
			t.Errorf("expected /private/tmp, got %q", got)
		}
	} else {
		if got != "/tmp" {
			t.Errorf("expected /tmp, got %q", got)
		}
	}
}

func TestExecute_CwdResetOnDelete(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	// Create a temp dir, cd into it, then remove it.
	dir, err := os.MkdirTemp("", "bash-test-*")
	if err != nil {
		t.Fatal(err)
	}

	_, err = bt.Execute(ctx, makeInput("cd "+dir))
	if err != nil {
		t.Fatal(err)
	}

	os.RemoveAll(dir)

	// Next command should reset to origin CWD.
	result, err := bt.Execute(ctx, makeInput("pwd -P"))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
}

func TestExecute_DurationMetadata(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	result, err := bt.Execute(ctx, makeInput("echo fast"))
	if err != nil {
		t.Fatal(err)
	}
	ms, ok := result.Metadata["duration_ms"]
	if !ok {
		t.Fatal("expected duration_ms in metadata")
	}
	if ms.(int64) < 0 {
		t.Errorf("duration_ms should be non-negative, got %v", ms)
	}
}

func TestExecute_BackgroundNotSupported(t *testing.T) {
	bt := newTestTool()
	ctx := context.Background()

	bg := true
	input, _ := json.Marshal(bashInput{Command: "echo bg", RunInBG: &bg})

	result, err := bt.Execute(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("background execution should return error")
	}
	if !strings.Contains(result.Content, "not yet supported") {
		t.Errorf("expected not-supported message, got %q", result.Content)
	}
}

// --- Helper function tests ---

func TestShellQuote(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"hello", "'hello'"},
		{"it's", "'it'\"'\"'s'"},
		{"/path/to/file", "'/path/to/file'"},
		{"", "''"},
	}
	for _, tc := range cases {
		got := shellQuote(tc.input)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello"
	if truncateOutput(short, 100) != short {
		t.Error("short output should not be truncated")
	}

	long := strings.Repeat("a\n", 100)
	result := truncateOutput(long, 50)
	if len(result) <= 50 {
		// The result includes the truncation notice, so it will be longer than maxLen.
		// Just check it was truncated.
	}
	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation notice")
	}
}

func TestCwd(t *testing.T) {
	bt := newTestTool()
	cwd := bt.Cwd()
	if cwd == "" {
		t.Error("expected non-empty CWD")
	}
}
