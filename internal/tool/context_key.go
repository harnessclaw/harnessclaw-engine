package tool

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// contextKey is the unexported key type for ToolUseContext in context.Context.
type contextKey struct{}

// toolUseContextKey is the singleton key used to store/retrieve ToolUseContext.
var toolUseContextKey = contextKey{}

// GetToolUseContext extracts the ToolUseContext from a context.Context.
// Returns the context and true if found, nil and false otherwise.
func GetToolUseContext(ctx context.Context) (*types.ToolUseContext, bool) {
	tuc, ok := ctx.Value(toolUseContextKey).(*types.ToolUseContext)
	return tuc, ok
}

// WithToolUseContext returns a child context carrying the ToolUseContext.
func WithToolUseContext(ctx context.Context, tuc *types.ToolUseContext) context.Context {
	return context.WithValue(ctx, toolUseContextKey, tuc)
}

// eventOutKey is the unexported key type for the event output channel.
type eventOutKey struct{}

// eventOutContextKey is the singleton key used to store/retrieve the event output channel.
var eventOutContextKey = eventOutKey{}

// GetEventOut extracts the parent event output channel from a context.
// Tools that need to emit events to the parent query loop (e.g., Agent tool)
// use this to get the streaming output channel.
func GetEventOut(ctx context.Context) (chan<- types.EngineEvent, bool) {
	ch, ok := ctx.Value(eventOutContextKey).(chan<- types.EngineEvent)
	return ch, ok
}

// WithEventOut returns a child context carrying the event output channel.
func WithEventOut(ctx context.Context, out chan<- types.EngineEvent) context.Context {
	return context.WithValue(ctx, eventOutContextKey, out)
}

// ArtifactStore is the interface that tools use to access stored artifacts.
// Defined here (in the tool package) to avoid circular dependencies between
// the tool layer and the artifact package.
type ArtifactStore interface {
	// Get returns the full content of a stored artifact by ID, or nil if not found.
	Get(id string) ArtifactContent
}

// ArtifactContent holds the retrieved artifact data.
type ArtifactContent struct {
	ID      string
	Content string
	Size    int
}

// artifactStoreKey is the unexported key type for the artifact store.
type artifactStoreKey struct{}

// artifactStoreContextKey is the singleton key used to store/retrieve the artifact store.
var artifactStoreContextKey = artifactStoreKey{}

// GetArtifactStore extracts the artifact store from a context.
func GetArtifactStore(ctx context.Context) (ArtifactStore, bool) {
	s, ok := ctx.Value(artifactStoreContextKey).(ArtifactStore)
	return s, ok
}

// WithArtifactStore returns a child context carrying the artifact store.
func WithArtifactStore(ctx context.Context, store ArtifactStore) context.Context {
	return context.WithValue(ctx, artifactStoreContextKey, store)
}
