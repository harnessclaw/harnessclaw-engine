package llmcall

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"harnessclaw-go/pkg/types"
)

func TestSanitizeTruncatedToolCalls_RewritesPartialJSON(t *testing.T) {
	core, recorded := observer.New(zap.WarnLevel)
	logger := zap.New(core)

	res := &LLMCallResult{
		ToolCalls: []types.ToolCall{
			{ID: "t1", Name: "write", Input: `{"file_path":"/tmp/x.js","content":"const {\n  Document`},
			{ID: "t2", Name: "read", Input: `{"path":"/tmp/y"}`},
		},
	}
	sanitizeTruncatedToolCalls(res, "agent-x", nil, logger)

	if res.ToolCalls[0].Input != "{}" {
		t.Errorf("t1 Input = %q, want {}", res.ToolCalls[0].Input)
	}
	if res.ToolCalls[0].ID != "t1" || res.ToolCalls[0].Name != "write" {
		t.Errorf("t1 (id,name) lost: id=%q name=%q", res.ToolCalls[0].ID, res.ToolCalls[0].Name)
	}
	if res.ToolCalls[1].Input != `{"path":"/tmp/y"}` {
		t.Errorf("t2 Input mutated: got %q", res.ToolCalls[1].Input)
	}

	logs := recorded.FilterMessageSnippet("tool_call input truncated").All()
	if len(logs) != 1 {
		t.Fatalf("expected 1 warn log, got %d", len(logs))
	}
	fields := logs[0].ContextMap()
	if fields["tool_use_id"] != "t1" {
		t.Errorf("warn log should reference t1, got %v", fields["tool_use_id"])
	}
}

func TestSanitizeTruncatedToolCalls_EmptyInputLeftAlone(t *testing.T) {
	res := &LLMCallResult{
		ToolCalls: []types.ToolCall{
			{ID: "t1", Name: "list_loaded_skills", Input: ""},
		},
	}
	sanitizeTruncatedToolCalls(res, "agent-x", nil, zap.NewNop())
	if res.ToolCalls[0].Input != "" {
		t.Errorf("empty Input should remain empty; got %q", res.ToolCalls[0].Input)
	}
}

func TestSanitizeTruncatedToolCalls_NilSafe(t *testing.T) {
	sanitizeTruncatedToolCalls(nil, "agent-x", nil, zap.NewNop())
	sanitizeTruncatedToolCalls(&LLMCallResult{}, "agent-x", nil, zap.NewNop())
}
