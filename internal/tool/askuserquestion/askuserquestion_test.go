package askuserquestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/tool"
)

func TestTool_Metadata(t *testing.T) {
	tl := New(zap.NewNop())
	if tl.Name() != "AskUserQuestion" {
		t.Errorf("Name() = %q, want AskUserQuestion", tl.Name())
	}
	if !tl.IsReadOnly() {
		t.Error("IsReadOnly should be true (asking the user mutates nothing)")
	}
	if tl.IsConcurrencySafe() {
		t.Error("IsConcurrencySafe should be false (one question at a time)")
	}
	if !tl.IsLongRunning() {
		t.Error("IsLongRunning should be true (blocks on user)")
	}
}

func TestTool_Schema(t *testing.T) {
	schema := New(zap.NewNop()).InputSchema()
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v", schema["type"])
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "question" {
		t.Errorf("required = %v, want [question]", required)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"question", "options", "multi", "allow_custom"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing property %q", key)
		}
	}
}

func TestTool_Validate(t *testing.T) {
	tl := New(zap.NewNop())
	cases := []struct {
		name    string
		input   string
		wantErr string // substring; "" = no error
	}{
		{
			name:    "valid minimal",
			input:   `{"question":"周五的会议在几点？"}`,
			wantErr: "",
		},
		{
			name:    "valid with options",
			input:   `{"question":"用哪种语气？","options":[{"label":"正式"},{"label":"轻松"}]}`,
			wantErr: "",
		},
		{
			name:    "valid with all fields",
			input:   `{"question":"选哪几个？","options":[{"label":"A","description":"方案A"},{"label":"B"}],"multi":true,"allow_custom":false}`,
			wantErr: "",
		},
		{
			name:    "missing question",
			input:   `{}`,
			wantErr: "question is required",
		},
		{
			name:    "blank question",
			input:   `{"question":"   "}`,
			wantErr: "question is required",
		},
		{
			name:    "option missing label",
			input:   `{"question":"x","options":[{"description":"only desc"}]}`,
			wantErr: "options[0].label is required",
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: "invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tl.ValidateInput(json.RawMessage(tc.input))
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tc.wantErr)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestTool_CheckPermission_AutoAllow(t *testing.T) {
	tl := New(zap.NewNop())
	res := tl.CheckPermission(context.Background(), json.RawMessage(`{"question":"x"}`))
	if res.Behavior != "allow" {
		t.Errorf("Behavior = %q, want allow", res.Behavior)
	}
}

func TestTool_InterruptBehavior(t *testing.T) {
	tl := New(zap.NewNop())
	if tl.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior = %v, want cancel", tl.InterruptBehavior())
	}
}

// TestTool_Execute_ServerSideFallback documents the expected behaviour of
// the server-side Execute path: it must NOT silently succeed because a
// truly server-only run has no human to ask. We expect a clear error
// message instead.
func TestTool_Execute_ServerSideFallback(t *testing.T) {
	tl := New(zap.NewNop())

	res, err := tl.Execute(context.Background(),
		json.RawMessage(`{"question":"would this hang?"}`))
	if err != nil {
		t.Fatalf("Execute returned non-nil error (should be in ToolResult.IsError): %v", err)
	}
	if !res.IsError {
		t.Error("Execute should set IsError=true in server-only mode")
	}
	if !strings.Contains(res.Content, "client_tools=true") {
		t.Errorf("error content should mention client_tools=true; got %q", res.Content)
	}
}

// TestTool_Execute_ValidatesInputFirst guards against the path where
// Execute is reached with malformed input — it must surface the validation
// error rather than the generic "no client" message.
func TestTool_Execute_ValidatesInputFirst(t *testing.T) {
	tl := New(zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError {
		t.Error("blank input should produce IsError=true")
	}
	if !strings.Contains(res.Content, "question is required") {
		t.Errorf("expected validation error in content; got %q", res.Content)
	}
}
