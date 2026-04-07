// Package tool defines the Tool interface and ToolResult type.
//
// Each tool (Bash, FileRead, FileEdit, etc.) implements this interface.
// Tools are registered with the ToolRegistry and invoked by the engine
// during the query loop when the LLM requests tool use.
package tool

import (
	"context"
	"encoding/json"

	"harnessclaw-go/pkg/types"
)

// Tool defines a callable tool that the LLM can invoke.
type Tool interface {
	// Name returns the unique tool identifier.
	Name() string

	// Description returns a human-readable description shown to the LLM.
	Description() string

	// InputSchema returns the JSON Schema for the tool's input parameters.
	InputSchema() map[string]any

	// Execute runs the tool with the given JSON input.
	Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error)

	// IsReadOnly returns true if the tool only reads state (safe for parallel execution).
	IsReadOnly() bool

	// IsEnabled reports whether this tool is currently active.
	// Tools may be disabled by configuration or feature gates.
	IsEnabled() bool

	// IsConcurrencySafe returns true if this tool can be executed in parallel
	// with other concurrency-safe tools. ReadOnly tools are always safe;
	// some write tools may also opt in (e.g., tools writing to isolated paths).
	IsConcurrencySafe() bool

	// ValidateInput checks the raw JSON input before execution.
	// Returns nil if valid; returns an error describing the validation failure.
	ValidateInput(input json.RawMessage) error
}

// BaseTool provides default implementations for the optional Tool methods.
// Embed this in concrete tool structs to get sensible defaults.
type BaseTool struct{}

func (BaseTool) IsEnabled() bool                          { return true }
func (BaseTool) IsConcurrencySafe() bool                  { return false }
func (BaseTool) ValidateInput(_ json.RawMessage) error    { return nil }
