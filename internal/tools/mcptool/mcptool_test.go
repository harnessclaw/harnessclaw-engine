package mcptool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"harnessclaw-go/internal/mcp"
	"harnessclaw-go/internal/tools"
)

// fakeCaller records what the adapter forwarded and returns a canned
// reply, so the adapter can be tested with no MCP server involved.
type fakeCaller struct {
	gotServer string
	gotTool   string
	gotArgs   map[string]any
	res       *mcp.CallResult
	err       error
}

func (f *fakeCaller) CallTool(_ context.Context, server, toolName string, args map[string]any) (*mcp.CallResult, error) {
	f.gotServer, f.gotTool, f.gotArgs = server, toolName, args
	return f.res, f.err
}

func desc() mcp.ToolDescriptor {
	return mcp.ToolDescriptor{
		Server:      "fake",
		Name:        "lookup",
		Description: "Look something up.",
		InputSchema: map[string]any{"type": "object"},
	}
}

func TestQualifiedName(t *testing.T) {
	if got := QualifiedName("fake", "lookup"); got != "mcp__fake__lookup" {
		t.Errorf("got %q", got)
	}
}

func TestTool_SatisfiesEngineInterface(t *testing.T) {
	var _ tool.Tool = New(&fakeCaller{}, desc())
}

func TestTool_NameAndDescription(t *testing.T) {
	tl := New(&fakeCaller{}, desc())

	if tl.Name() != "mcp__fake__lookup" {
		t.Errorf("name: got %q", tl.Name())
	}
	// Origin must be visible to the LLM so it can tell a built-in from a
	// call that crosses into third-party code.
	if !strings.Contains(tl.Description(), "Look something up.") {
		t.Errorf("description lost the server text: %q", tl.Description())
	}
	if !strings.Contains(tl.Description(), "fake") {
		t.Errorf("description should name the server: %q", tl.Description())
	}
}

func TestTool_DescriptionFallbackWhenServerGivesNone(t *testing.T) {
	d := desc()
	d.Description = "   "
	got := New(&fakeCaller{}, d).Description()
	if !strings.Contains(got, "lookup") || !strings.Contains(got, "fake") {
		t.Errorf("expected a synthesised description, got %q", got)
	}
}

// A server's readOnlyHint must never grant an auto-allow: it is
// self-attested by third-party code, and IsReadOnly feeds the permission
// pipeline's ReadOnlyAutoAllow step.
func TestTool_ReadOnlyHintNeverBypassesPermission(t *testing.T) {
	d := desc()
	d.ReadOnly = true
	tl := New(&fakeCaller{}, d)

	if tl.IsReadOnly() {
		t.Error("IsReadOnly must stay false even when the server claims read-only")
	}
	if tl.IsConcurrencySafe() {
		t.Error("IsConcurrencySafe must stay false")
	}
	// The hint is not discarded — it lands on SafetyLevel instead.
	if got := tl.SafetyLevel(); got != tool.SafetyCaution {
		t.Errorf("hinted read-only should be Caution, got %q", got)
	}
}

func TestTool_SafetyLevelDangerousWithoutHint(t *testing.T) {
	if got := New(&fakeCaller{}, desc()).SafetyLevel(); got != tool.SafetyDangerous {
		t.Errorf("got %q want dangerous", got)
	}
	// Never SafetySafe: that tier is for tools the engine implements.
	if New(&fakeCaller{}, desc()).SafetyLevel() == tool.SafetySafe {
		t.Error("MCP tools must never claim SafetySafe")
	}
}

func TestTool_InputSchemaPassedThroughUntouched(t *testing.T) {
	d := desc()
	d.InputSchema = map[string]any{"type": "object", "required": []any{"q"}}
	got := New(&fakeCaller{}, d).InputSchema()
	if got["required"] == nil {
		t.Errorf("schema was rewritten: %+v", got)
	}
}

func TestTool_ValidateInput(t *testing.T) {
	tl := New(&fakeCaller{}, desc())

	for _, in := range []string{"", "   ", `{"q":"x"}`, "{}"} {
		if err := tl.ValidateInput(json.RawMessage(in)); err != nil {
			t.Errorf("ValidateInput(%q) = %v, want nil", in, err)
		}
	}
	if err := tl.ValidateInput(json.RawMessage("not json")); err == nil {
		t.Error("malformed JSON should be rejected")
	}
}

func TestTool_ExecuteForwardsServerAndArgs(t *testing.T) {
	fc := &fakeCaller{res: &mcp.CallResult{Text: "found it"}}
	tl := New(fc, desc())

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"q":"vLLM"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fc.gotServer != "fake" || fc.gotTool != "lookup" {
		t.Errorf("routed to %q/%q", fc.gotServer, fc.gotTool)
	}
	if fc.gotArgs["q"] != "vLLM" {
		t.Errorf("args: %+v", fc.gotArgs)
	}
	if res.Content != "found it" || res.IsError {
		t.Errorf("result: %+v", res)
	}
}

func TestTool_ExecuteEmptyInputSendsNoArgs(t *testing.T) {
	for _, in := range []string{"", "null"} {
		fc := &fakeCaller{res: &mcp.CallResult{Text: "ok"}}
		if _, err := New(fc, desc()).Execute(context.Background(), json.RawMessage(in)); err != nil {
			t.Fatalf("Execute(%q): %v", in, err)
		}
		if len(fc.gotArgs) != 0 {
			t.Errorf("Execute(%q) sent args %+v", in, fc.gotArgs)
		}
	}
}

// A tool that ran and failed becomes an error ToolResult the LLM can
// read; a request that never completed becomes a Go error so toolexec
// can classify it (deadline → ToolErrorToolTimeout).
func TestTool_ExecuteDistinguishesToolErrorFromTransportError(t *testing.T) {
	t.Run("tool-level error", func(t *testing.T) {
		fc := &fakeCaller{res: &mcp.CallResult{Text: "bad query", IsError: true}}
		res, err := New(fc, desc()).Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("should not be a Go error: %v", err)
		}
		if !res.IsError || res.Content != "bad query" {
			t.Errorf("result: %+v", res)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		boom := errors.New("broken pipe")
		fc := &fakeCaller{err: boom}
		_, err := New(fc, desc()).Execute(context.Background(), json.RawMessage(`{}`))
		if !errors.Is(err, boom) {
			t.Errorf("got %v want the transport error", err)
		}
	})
}

// An empty result reads to the LLM as a silent no-op and invites a
// pointless retry, so it is replaced with an explicit statement.
func TestTool_ExecuteEmptyOutputIsExplained(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fc := &fakeCaller{res: &mcp.CallResult{Text: "  "}}
		res, _ := New(fc, desc()).Execute(context.Background(), json.RawMessage(`{}`))
		if !strings.Contains(res.Content, "no output") {
			t.Errorf("got %q", res.Content)
		}
		if res.IsError {
			t.Error("empty output is not an error")
		}
	})

	t.Run("error", func(t *testing.T) {
		fc := &fakeCaller{res: &mcp.CallResult{Text: "", IsError: true}}
		res, _ := New(fc, desc()).Execute(context.Background(), json.RawMessage(`{}`))
		if !res.IsError || !strings.Contains(res.Content, "no message") {
			t.Errorf("got %+v", res)
		}
	})
}

func TestTool_ExecuteRejectsMalformedInput(t *testing.T) {
	fc := &fakeCaller{res: &mcp.CallResult{Text: "unused"}}
	if _, err := New(fc, desc()).Execute(context.Background(), json.RawMessage(`["not","an","object"]`)); err == nil {
		t.Error("array input should fail to decode into arguments")
	}
}

func TestNewTools_NilManagerIsSafe(t *testing.T) {
	if got := NewTools(nil); got != nil {
		t.Errorf("got %v want nil", got)
	}
}
