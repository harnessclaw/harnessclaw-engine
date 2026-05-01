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
// fix: when AvailableTools is set (e.g. Specialists's whitelist of
// Task/WebSearch/TavilySearch), ToolsSection must render only those — not the
// global registry. Otherwise the prompt advertises tools the LLM can't call.
func TestToolsSection_PrefersAvailableTools(t *testing.T) {
	registry := tool.NewRegistry()
	for _, name := range []string{"Bash", "Edit", "Read", "Write", "Task", "WebSearch", "TavilySearch"} {
		_ = registry.Register(&fakeTool{name: name, desc: "stub"})
	}

	available := []tool.Tool{
		&fakeTool{name: "Task", desc: "spawn L3"},
		&fakeTool{name: "WebSearch", desc: "search the web"},
		&fakeTool{name: "TavilySearch", desc: "tavily search"},
	}

	s := NewToolsSection()
	out, err := s.Render(&prompt.PromptContext{
		Tools:          registry,
		AvailableTools: available,
	}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, must := range []string{"Task", "WebSearch", "TavilySearch"} {
		if !strings.Contains(out, "**"+must+"**") {
			t.Errorf("expected whitelisted tool %q in output, got:\n%s", must, out)
		}
	}
	for _, mustNot := range []string{"Bash", "Edit", "Read", "Write"} {
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
	_ = registry.Register(&fakeTool{name: "Bash", desc: "shell"})
	_ = registry.Register(&fakeTool{name: "Read", desc: "read file"})

	s := NewToolsSection()
	out, err := s.Render(&prompt.PromptContext{Tools: registry}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, must := range []string{"Bash", "Read"} {
		if !strings.Contains(out, "**"+must+"**") {
			t.Errorf("registry fallback missing %q:\n%s", must, out)
		}
	}
}
