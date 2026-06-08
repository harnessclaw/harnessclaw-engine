package tool

import (
	"context"
	"encoding/json"
	"testing"

	"harnessclaw-go/pkg/types"
)

// fakeTool is a minimal Tool implementation for registry tests.
// BaseTool provides IsConcurrencySafe() and ValidateInput() defaults
// so fakeTool satisfies the full Tool interface without extra boilerplate.
type fakeTool struct {
	BaseTool
	name    string
	enabled bool
}

func (f *fakeTool) Name() string                { return f.name }
func (f *fakeTool) Description() string         { return "fake " + f.name }
func (f *fakeTool) IsEnabled() bool             { return f.enabled }
func (f *fakeTool) IsReadOnly() bool            { return true }
func (f *fakeTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "fake"}, nil
}

func TestRegistry_Replace_SwapsExisting(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeTool{name: "X", enabled: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	repl := &fakeTool{name: "X", enabled: false}
	if err := r.Replace("X", repl); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got := r.Get("X")
	if got != repl {
		t.Errorf("Get returned wrong instance after Replace: got %p, want %p", got, repl)
	}
}

func TestRegistry_Replace_RejectsNameMismatch(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeTool{name: "X", enabled: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Replace("X", &fakeTool{name: "Y", enabled: true}); err == nil {
		t.Fatal("expected Replace to reject name mismatch, got nil")
	}
}

func TestRegistry_Replace_RejectsUnknown(t *testing.T) {
	r := NewRegistry()
	if err := r.Replace("X", &fakeTool{name: "X", enabled: true}); err == nil {
		t.Fatal("expected Replace to reject unknown name, got nil")
	}
}

func TestRegistry_Replace_RejectsNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeTool{name: "X", enabled: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Replace("X", nil); err == nil {
		t.Fatal("expected Replace to reject nil, got nil")
	}
}
