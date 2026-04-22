package artifacttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/tool"
)

// mockStore implements tool.ArtifactStore for testing.
type mockStore struct {
	artifacts map[string]tool.ArtifactContent
}

func (m *mockStore) Get(id string) tool.ArtifactContent {
	return m.artifacts[id]
}

func newMockStore(items map[string]tool.ArtifactContent) *mockStore {
	return &mockStore{artifacts: items}
}

func TestGetToolName(t *testing.T) {
	g := NewGetTool()
	if g.Name() != "ArtifactGet" {
		t.Errorf("Name() = %q, want ArtifactGet", g.Name())
	}
}

func TestGetToolIsReadOnly(t *testing.T) {
	g := NewGetTool()
	if !g.IsReadOnly() {
		t.Error("ArtifactGet should be read-only")
	}
}

func TestGetToolIsConcurrencySafe(t *testing.T) {
	g := NewGetTool()
	if !g.IsConcurrencySafe() {
		t.Error("ArtifactGet should be concurrency-safe")
	}
}

func TestGetToolInputSchema(t *testing.T) {
	g := NewGetTool()
	schema := g.InputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	if _, ok := props["artifact_id"]; !ok {
		t.Error("schema should have artifact_id property")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema should have required array")
	}
	if len(required) != 1 || required[0] != "artifact_id" {
		t.Errorf("required = %v, want [artifact_id]", required)
	}
}

func TestGetToolValidateInput(t *testing.T) {
	g := NewGetTool()

	// Valid input.
	valid := json.RawMessage(`{"artifact_id": "art_abc12345"}`)
	if err := g.ValidateInput(valid); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}

	// Missing artifact_id.
	missing := json.RawMessage(`{}`)
	if err := g.ValidateInput(missing); err == nil {
		t.Error("missing artifact_id should be rejected")
	}

	// Empty artifact_id.
	empty := json.RawMessage(`{"artifact_id": ""}`)
	if err := g.ValidateInput(empty); err == nil {
		t.Error("empty artifact_id should be rejected")
	}

	// Invalid JSON.
	invalid := json.RawMessage(`not json`)
	if err := g.ValidateInput(invalid); err == nil {
		t.Error("invalid JSON should be rejected")
	}
}

func TestGetToolExecuteSuccess(t *testing.T) {
	g := NewGetTool()
	store := newMockStore(map[string]tool.ArtifactContent{
		"art_abc12345": {ID: "art_abc12345", Content: "full content here", Size: 17},
	})

	ctx := tool.WithArtifactStore(context.Background(), store)
	input := json.RawMessage(`{"artifact_id": "art_abc12345"}`)

	result, err := g.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}
	if result.Content != "full content here" {
		t.Errorf("content = %q, want 'full content here'", result.Content)
	}
	if result.Metadata["artifact_id"] != "art_abc12345" {
		t.Errorf("metadata artifact_id = %v, want art_abc12345", result.Metadata["artifact_id"])
	}
	if result.Metadata["size"] != 17 {
		t.Errorf("metadata size = %v, want 17", result.Metadata["size"])
	}
}

func TestGetToolExecuteNotFound(t *testing.T) {
	g := NewGetTool()
	store := newMockStore(map[string]tool.ArtifactContent{})

	ctx := tool.WithArtifactStore(context.Background(), store)
	input := json.RawMessage(`{"artifact_id": "art_nonexistent"}`)

	result, err := g.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for not-found artifact")
	}
	if !strings.Contains(result.Content, "not found") {
		t.Errorf("content = %q, should mention 'not found'", result.Content)
	}
}

func TestGetToolExecuteNoStore(t *testing.T) {
	g := NewGetTool()

	// Context without artifact store.
	ctx := context.Background()
	input := json.RawMessage(`{"artifact_id": "art_abc12345"}`)

	result, err := g.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error when artifact store is not available")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("content = %q, should mention 'not available'", result.Content)
	}
}

func TestGetToolExecuteInvalidInput(t *testing.T) {
	g := NewGetTool()
	store := newMockStore(map[string]tool.ArtifactContent{})
	ctx := tool.WithArtifactStore(context.Background(), store)

	input := json.RawMessage(`invalid json`)
	result, err := g.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for invalid input")
	}
}

func TestGetToolDescription(t *testing.T) {
	g := NewGetTool()
	desc := g.Description()
	if desc == "" {
		t.Error("description should not be empty")
	}
	if !strings.Contains(desc, "artifact") {
		t.Error("description should mention artifact")
	}
}
