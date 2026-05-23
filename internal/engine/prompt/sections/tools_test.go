package sections

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeTool is a minimal tool stub for asserting ToolsSection rendering.
type fakeTool struct {
	tool.BaseTool
	name string
	desc string
}

func (f *fakeTool) Name() string                                     { return f.name }
func (f *fakeTool) Description() string                              { return f.desc }
func (f *fakeTool) InputSchema() map[string]any                      { return map[string]any{} }
func (f *fakeTool) IsReadOnly() bool                                 { return true }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{}, nil
}

// TestToolsSection_PrefersAvailableTools guards the L2/L3 prompt-runtime sync
// fix: when AvailableTools is set (e.g. scheduler's whitelist of
// task/web_search/tavily_search), ToolsSection must render only those — not the
// global registry. Otherwise the prompt advertises tools the LLM can't call.
func TestToolsSection_PrefersAvailableTools(t *testing.T) {
	registry := tool.NewRegistry()
	for _, name := range []string{"bash", "edit", "read", "write", "task", "web_search", "tavily_search"} {
		_ = registry.Register(&fakeTool{name: name, desc: "stub"})
	}

	available := []tool.Tool{
		&fakeTool{name: "task", desc: "spawn L3"},
		&fakeTool{name: "web_search", desc: "search the web"},
		&fakeTool{name: "tavily_search", desc: "tavily search"},
	}

	s := NewToolsSection()
	out, err := s.Render(&prompt.PromptContext{
		Tools:          registry,
		AvailableTools: available,
	}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, must := range []string{"task", "web_search", "tavily_search"} {
		if !strings.Contains(out, "**"+must+"**") {
			t.Errorf("expected whitelisted tool %q in output, got:\n%s", must, out)
		}
	}
	for _, mustNot := range []string{"bash", "edit", "read", "write"} {
		if strings.Contains(out, "**"+mustNot+"**") {
			t.Errorf("global-registry tool %q leaked into prompt despite AvailableTools whitelist:\n%s", mustNot, out)
		}
	}
}

// TestToolsSection_FallsBackToRegistry covers the main-agent path where no
// runtime filter is applied (AvailableTools is nil) — the section should
// still render the full registry.
func TestToolsSection_FallsBackToRegistry(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(&fakeTool{name: "bash", desc: "shell"})
	_ = registry.Register(&fakeTool{name: "read", desc: "read file"})

	s := NewToolsSection()
	out, err := s.Render(&prompt.PromptContext{Tools: registry}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, must := range []string{"bash", "read"} {
		if !strings.Contains(out, "**"+must+"**") {
			t.Errorf("registry fallback missing %q:\n%s", must, out)
		}
	}
}
