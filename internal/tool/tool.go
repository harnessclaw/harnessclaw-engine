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

func (BaseTool) IsEnabled() bool                       { return true }
func (BaseTool) IsConcurrencySafe() bool               { return false }
func (BaseTool) ValidateInput(_ json.RawMessage) error { return nil }

// ---------------------------------------------------------------------------
// Optional interfaces — checked via type assertion at runtime.
// This follows the Go idiom of small interfaces (cf. io.Closer, http.Flusher).
// The core Tool interface stays lean; tools opt in to extra capabilities.
// ---------------------------------------------------------------------------

// AliasedTool provides alternative names for backward-compatible lookup.
// Example: "Bash" tool might have aliases ["Shell", "BashTool"].
type AliasedTool interface {
	Aliases() []string
}

// PromptProvider supplies system prompt text describing this tool's usage.
// The returned string is included in the system prompt sent to the LLM.
type PromptProvider interface {
	Prompt(ctx context.Context) string
}

// SearchHintProvider enables ToolSearch keyword matching.
// Returns a short text hint used when the ToolSearch mechanism needs
// to find tools by keyword rather than exact name.
type SearchHintProvider interface {
	SearchHint() string
}

// DeferredTool marks tools that support deferred loading via ToolSearch.
// When ShouldDefer returns true, the tool's schema is not included in the
// initial tool list; instead it is loaded on demand.
type DeferredTool interface {
	ShouldDefer() bool
}

// InterruptMode defines behavior when the user submits new input mid-execution.
type InterruptMode string

const (
	// InterruptCancel means the tool execution is cancelled immediately.
	InterruptCancel InterruptMode = "cancel"
	// InterruptBlock means the tool blocks new user input until completion.
	InterruptBlock InterruptMode = "block"
)

// InterruptibleTool declares the tool's behavior on user interrupt.
type InterruptibleTool interface {
	InterruptBehavior() InterruptMode
}

// ResultSizeLimiter caps the tool output size before sending to the model.
// If the result exceeds MaxResultSizeChars, the engine truncates or persists
// the full output and sends a summary.
type ResultSizeLimiter interface {
	MaxResultSizeChars() int
}

// PermissionPreResult carries the outcome of a tool-specific permission pre-check.
type PermissionPreResult struct {
	// Behavior is one of "allow", "deny", "ask", "passthrough".
	Behavior string
	// Message provides a human-readable explanation.
	Message string
	// UpdatedInput may modify the original input (nil = no change).
	UpdatedInput json.RawMessage
}

// PermissionPreChecker lets a tool provide tool-specific permission logic
// that runs before the general permission pipeline.
type PermissionPreChecker interface {
	CheckPermission(ctx context.Context, input json.RawMessage) PermissionPreResult
}

// ClientRoutedTool marks tools whose execution **must** happen on the
// connected client (e.g. AskUserQuestion, which can only render in the UI
// where the human is sitting). When IsClientRouted returns true, the
// engine sends a tool.call wire message regardless of the global
// QueryEngineConfig.ClientTools flag, and never falls through to the
// tool's server-side Execute() method.
//
// Tools that can run on either side (Bash, Read, Edit, Write — used in
// the Claude Code CLI delegation model) should NOT implement this; they
// are routed by the global ClientTools flag, which matches the
// "everything client" delegation contract those tools were designed for.
type ClientRoutedTool interface {
	IsClientRouted() bool
}

// ContextModifier lets a tool modify the execution context after completion.
// This is used by tools like SkillTool to inject allowedTools, model overrides, etc.
type ContextModifier interface {
	ModifyContext(tuc *types.ToolUseContext) *types.ToolUseContext
}

// SearchReadInfo classifies a tool invocation as search/read/list for UI folding.
type SearchReadInfo struct {
	IsSearch bool
	IsRead   bool
	IsList   bool
}

// SearchOrReadClassifier marks read/search operations for UI grouping.
type SearchOrReadClassifier interface {
	IsSearchOrReadCommand(input json.RawMessage) SearchReadInfo
}

// DestructiveMarker flags irreversible operations for extra safety checks.
type DestructiveMarker interface {
	IsDestructive(input json.RawMessage) bool
}

// LongRunningTool marks tools that manage their own timeout and should bypass
// the executor's default timeout. The Agent tool uses this because sub-agent
// execution may take much longer than a single tool invocation.
type LongRunningTool interface {
	IsLongRunning() bool
}
