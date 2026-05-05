package submittool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEscalateTool_Identity(t *testing.T) {
	tl := NewEscalate()
	if tl.Name() != EscalateToolName {
		t.Errorf("Name() = %q, want %q", tl.Name(), EscalateToolName)
	}
	if tl.IsConcurrencySafe() {
		t.Error("IsConcurrencySafe() should be false (terminal action)")
	}
	if tl.IsReadOnly() {
		t.Error("IsReadOnly() should be false")
	}
}

func TestEscalateTool_InputSchema_RequiresReason(t *testing.T) {
	schema := NewEscalate().InputSchema()
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "reason" {
		t.Errorf("required = %v, want [reason]", required)
	}
}

func TestEscalateTool_ValidateInput_RejectsEmpty(t *testing.T) {
	tl := NewEscalate()
	cases := []struct {
		name string
		in   string
	}{
		{"empty json", `{}`},
		{"empty reason", `{"reason": ""}`},
		{"whitespace reason", `{"reason": "   "}`},
		{"too long", `{"reason": "` + strings.Repeat("a", EscalateMaxReasonChars+1) + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := tl.ValidateInput(json.RawMessage(c.in)); err == nil {
				t.Errorf("expected error for %q, got nil", c.in)
			}
		})
	}
}

func TestEscalateTool_ValidateInput_AcceptsValid(t *testing.T) {
	tl := NewEscalate()
	in := `{"reason": "missing input file", "suggested_next_steps": "ask user to upload"}`
	if err := tl.ValidateInput(json.RawMessage(in)); err != nil {
		t.Errorf("ValidateInput: %v", err)
	}
}

func TestEscalateTool_Execute_StampsRenderHint(t *testing.T) {
	tl := NewEscalate()
	in := `{"reason": "task is impossible as scoped", "suggested_next_steps": "split into two"}`
	res, err := tl.Execute(context.Background(), json.RawMessage(in))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Error("escalation should not set IsError — escalating IS a success outcome")
	}
	hint, _ := res.Metadata["render_hint"].(string)
	if hint != EscalateMetadataRenderHint {
		t.Errorf("render_hint = %q, want %q (driver loop reads this to detect escalation)",
			hint, EscalateMetadataRenderHint)
	}
	reason, _ := res.Metadata["escalation_reason"].(string)
	if !strings.Contains(reason, "impossible as scoped") {
		t.Errorf("escalation_reason metadata = %q, want it to echo the input", reason)
	}
	steps, _ := res.Metadata["suggested_next_steps"].(string)
	if !strings.Contains(steps, "split into two") {
		t.Errorf("suggested_next_steps metadata = %q, want it to echo the input", steps)
	}
	// Body should carry status=needs_planning so the LLM (and any
	// log-replay tooling) sees the same signal as the driver.
	if !strings.Contains(res.Content, "needs_planning") {
		t.Errorf("body should contain status=needs_planning, got %q", res.Content)
	}
}

func TestEscalateTool_Description_HasGuidance(t *testing.T) {
	desc := NewEscalate().Description()
	// The description must surface the WHEN / DO NOT guidance pair so the
	// LLM has a clear "use" vs "abuse" rubric, and must reference the
	// peer terminal tool (SubmitTaskResult) so the model picks the right
	// exit when both are available.
	// Description got translated to Chinese (P0 prompt cleanup) — assert
	// the Chinese markers now ("何时" / "不要") plus the still-English
	// peer tool name and status.
	for _, want := range []string{"SubmitTaskResult", "何时", "不要", "needs_planning"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q (%d chars total)", want, len(desc))
		}
	}
}
