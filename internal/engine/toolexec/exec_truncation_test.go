package toolexec

import (
	"errors"
	"strings"
	"testing"
)

func TestWrapValidateErr_TruncationOnUnexpectedEOF(t *testing.T) {
	err := errors.New("invalid input: unexpected end of JSON input")
	out := wrapValidateErr("write", `{"file_path":"/x","content":"long...`, err)
	if !strings.Contains(out, "max_tokens") {
		t.Errorf("expected truncation hint mentioning max_tokens, got: %s", out)
	}
	if !strings.Contains(out, "DO NOT retry") {
		t.Errorf("expected anti-retry guidance, got: %s", out)
	}
}

// Regression: the file_path is required path. A SMALL valid JSON missing a
// field is a real LLM mistake, NOT truncation — the original concise error
// must be preserved so the model sees the actual schema problem.
func TestWrapValidateErr_PreservesSmallSchemaError(t *testing.T) {
	err := errors.New("file_path is required")
	out := wrapValidateErr("write", `{"content":"x"}`, err)
	if strings.Contains(out, "max_tokens") {
		t.Errorf("must NOT add truncation hint for small valid JSON, got: %s", out)
	}
	if out != "invalid input for write: file_path is required" {
		t.Errorf("unexpected wrapping: %s", out)
	}
}

// Regression: the field-empty-after-truncation path. A large input that
// happens to parse but has an empty required field IS truncation (LLM
// reorders fields and the second one gets cut). 4KB+ input with no
// closing brace is our signal.
func TestWrapValidateErr_TruncationOnLargeUnclosedInput(t *testing.T) {
	bigContent := strings.Repeat("x", 5000) // > 4KB, no closing brace
	rawInput := `{"content":"` + bigContent
	err := errors.New("file_path is required")
	out := wrapValidateErr("write", rawInput, err)
	if !strings.Contains(out, "max_tokens") {
		t.Errorf("expected truncation hint for large unclosed input, got: %s", out)
	}
}

func TestIsLikelyTruncation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		errMsg   string
		expected bool
	}{
		{"unexpected EOF", `{"x":1`, "unexpected end of JSON input", true},
		{"small closed JSON", `{"content":"x"}`, "file_path is required", false},
		{"large closed JSON", `{"file_path":"/x","content":"` + strings.Repeat("y", 5000) + `"}`, "some err", false},
		{"large unclosed JSON", `{"content":"` + strings.Repeat("z", 5000), "some err", true},
		{"empty input", "", "any err", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isLikelyTruncation(tc.input, tc.errMsg)
			if got != tc.expected {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}
