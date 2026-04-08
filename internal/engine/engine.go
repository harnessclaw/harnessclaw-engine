// Package engine implements the core query loop that orchestrates
// LLM calls, tool execution, and context management.
package engine

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// Engine processes user messages through the LLM query loop.
type Engine interface {
	// ProcessMessage handles a user message for the given session,
	// returning a channel of streaming events.
	ProcessMessage(ctx context.Context, sessionID string, msg *types.Message) (<-chan types.EngineEvent, error)

	// SubmitToolResult delivers a client-side tool execution result back to
	// the engine so the query loop can continue with the next LLM turn.
	SubmitToolResult(ctx context.Context, sessionID string, result *types.ToolResultPayload) error

	// SubmitPermissionResult delivers a permission approval/denial response
	// from the client to the waiting tool executor.
	SubmitPermissionResult(ctx context.Context, sessionID string, resp *types.PermissionResponse) error

	// AbortSession cancels any in-flight processing for a session.
	AbortSession(ctx context.Context, sessionID string) error
}
