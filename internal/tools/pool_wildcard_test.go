package tool

import (
	"context"
	"encoding/json"
	"testing"

	"harnessclaw-go/pkg/types"
)

// stubTool is a minimal Tool for pool-assembly assertions.
type stubTool struct {
	BaseTool
	name  string
	ready bool
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return s.name + " desc" }
func (s stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) IsReadOnly() bool            { return false }
func (s stubTool) IsEnabled() bool             { return s.ready }
func (s stubTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "ok"}, nil
}

func TestMatchesToolPattern(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything", true},
		{"bash", "bash", true},
		{"bash", "bash_extra", false},
		{"mcp__*", "mcp__fake__lookup", true},
		{"mcp__*", "mcp__other__x", true},
		{"mcp__*", "bash", false},
		{"mcp__fake__*", "mcp__fake__lookup", true},
		{"mcp__fake__*", "mcp__other__lookup", false},
		{"", "bash", false},
	}
	for _, c := range cases {
		if got := matchesToolPattern(c.pattern, c.name); got != c.want {
			t.Errorf("matchesToolPattern(%q, %q) = %v want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// The whole point of the wildcard: a definition whose AllowedTools is a
// finite list still has to be able to admit MCP tools whose names are
// only known at runtime.
func TestFilterByNames_WildcardAdmitsMCPTools(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubTool{name: "bash", ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(stubTool{name: "read", ready: true}); err != nil {
		t.Fatal(err)
	}

	mcpTools := []Tool{
		stubTool{name: "mcp__fake__lookup", ready: true},
		stubTool{name: "mcp__fake__mutate", ready: true},
	}

	pool := NewToolPool(reg, mcpTools, nil)
	if pool.Size() != 4 {
		t.Fatalf("pool should hold 4 tools, got %d (%v)", pool.Size(), pool.Names())
	}

	t.Run("exact whitelist excludes MCP", func(t *testing.T) {
		got := pool.FilterByNames([]string{"bash"}).Names()
		if len(got) != 1 || got[0] != "bash" {
			t.Errorf("got %v want [bash]", got)
		}
	})

	t.Run("wildcard admits all MCP alongside exact names", func(t *testing.T) {
		got := pool.FilterByNames([]string{"bash", "mcp__*"}).Names()
		want := map[string]bool{"bash": true, "mcp__fake__lookup": true, "mcp__fake__mutate": true}
		if len(got) != len(want) {
			t.Fatalf("got %v want keys of %v", got, want)
		}
		for _, n := range got {
			if !want[n] {
				t.Errorf("unexpected tool %q", n)
			}
		}
	})

	t.Run("per-server wildcard narrows further", func(t *testing.T) {
		got := pool.FilterByNames([]string{"mcp__fake__look*"}).Names()
		if len(got) != 1 || got[0] != "mcp__fake__lookup" {
			t.Errorf("got %v want [mcp__fake__lookup]", got)
		}
	})

	t.Run("bare star keeps everything", func(t *testing.T) {
		if got := pool.FilterByNames([]string{"*"}).Names(); len(got) != 4 {
			t.Errorf("got %v want all 4", got)
		}
	})
}

// MCP tools must land AFTER the built-in block: the built-in prefix has
// to stay byte-stable for prompt caching as servers come and go.
func TestNewToolPool_MCPToolsAppendedAfterBuiltins(t *testing.T) {
	reg := NewRegistry()
	for _, n := range []string{"read", "bash"} {
		if err := reg.Register(stubTool{name: n, ready: true}); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately alphabetically-early so a single global sort would
	// hoist it above the built-ins and this test would catch it.
	pool := NewToolPool(reg, []Tool{stubTool{name: "aaa_mcp_tool", ready: true}}, nil)

	names := pool.Names()
	want := []string{"bash", "read", "aaa_mcp_tool"}
	if len(names) != len(want) {
		t.Fatalf("got %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("[%d]: got %q want %q", i, names[i], want[i])
		}
	}
}

func TestNewToolPool_SkipsDisabledMCPToolsAndDenyRules(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubTool{name: "bash", ready: true}); err != nil {
		t.Fatal(err)
	}
	pool := NewToolPool(reg,
		[]Tool{
			stubTool{name: "mcp__a__on", ready: true},
			stubTool{name: "mcp__a__off", ready: false},
			stubTool{name: "mcp__a__denied", ready: true},
		},
		[]string{"mcp__a__denied"},
	)

	got := pool.Names()
	want := map[string]bool{"bash": true, "mcp__a__on": true}
	if len(got) != len(want) {
		t.Fatalf("got %v want keys of %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected %q", n)
		}
	}
}
