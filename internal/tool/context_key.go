package tool

import (
	"context"
	"time"

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

// allowedSkillsKey is the unexported key type for the allowed skills whitelist.
type allowedSkillsKey struct{}

// allowedSkillsContextKey is the singleton key for allowed skills.
var allowedSkillsContextKey = allowedSkillsKey{}

// GetAllowedSkills extracts the allowed skills whitelist from a context.
// Returns nil, false if no restriction is set (all skills allowed).
func GetAllowedSkills(ctx context.Context) (map[string]bool, bool) {
	s, ok := ctx.Value(allowedSkillsContextKey).(map[string]bool)
	return s, ok
}

// WithAllowedSkills returns a child context carrying the allowed skills whitelist.
func WithAllowedSkills(ctx context.Context, skills map[string]bool) context.Context {
	return context.WithValue(ctx, allowedSkillsContextKey, skills)
}

// ArtifactProducer carries the identity stamp the engine attaches to
// artifacts written from this tool call. Tools never set this themselves;
// the executor populates it just before Execute, drawing on session/agent
// context. We keep the struct small and decoupled from the artifact
// package to avoid an artifact→tool→artifact import cycle.
type ArtifactProducer struct {
	AgentID    string
	AgentRunID string
	TaskID     string
	SessionID  string
	TraceID    string
}

// artifactProducerKey is the unexported key for ArtifactProducer.
type artifactProducerKey struct{}

var artifactProducerContextKey = artifactProducerKey{}

// GetArtifactProducer returns the producer stamp injected by the engine.
// Returns zero value + false when not present (e.g. tool unit tests).
func GetArtifactProducer(ctx context.Context) (ArtifactProducer, bool) {
	p, ok := ctx.Value(artifactProducerContextKey).(ArtifactProducer)
	return p, ok
}

// WithArtifactProducer attaches a producer stamp to ctx.
func WithArtifactProducer(ctx context.Context, p ArtifactProducer) context.Context {
	return context.WithValue(ctx, artifactProducerContextKey, p)
}

// artifactStoreCtxKey is unexported and points at any value — the artifact
// package's *Store. Stored as `any` so we don't introduce a tool→artifact
// import here; the artifact tools type-assert when reading.
type artifactStoreCtxKey struct{}

var artifactStoreContextKey = artifactStoreCtxKey{}

// GetArtifactStoreValue returns whatever value the engine stashed under
// the artifact-store key. Callers in the artifact tool layer assert it
// to the concrete *artifact.Store type.
func GetArtifactStoreValue(ctx context.Context) (any, bool) {
	v := ctx.Value(artifactStoreContextKey)
	if v == nil {
		return nil, false
	}
	return v, true
}

// WithArtifactStoreValue attaches a Store handle to ctx. Engine code
// passes the concrete *artifact.Store; the helper stays type-agnostic so
// the tool package doesn't have to import artifact.
func WithArtifactStoreValue(ctx context.Context, store any) context.Context {
	return context.WithValue(ctx, artifactStoreContextKey, store)
}

// TaskContract is the deliverable contract attached to a sub-agent's
// dispatch. The framework injects it via ctx so SubmitTaskResult can
// validate submitted artifacts against the parent's expectations
// (doc §3 mechanisms M3/M4).
//
// Distinguished from ArtifactProducer: producer is per-tool-call lineage
// stamp (small, copied into every artifact); contract is task-level
// (heavier, read-only by the validating tool).
type TaskContract struct {
	TaskID          string
	TaskStartedAt   time.Time
	ExpectedOutputs []types.ExpectedOutput
}

type taskContractKey struct{}

var taskContractContextKey = taskContractKey{}

// GetTaskContract returns the task contract attached to ctx, or zero
// value + false when absent (legacy / no-contract dispatches).
func GetTaskContract(ctx context.Context) (TaskContract, bool) {
	c, ok := ctx.Value(taskContractContextKey).(TaskContract)
	return c, ok
}

// WithTaskContract attaches a task contract to ctx.
func WithTaskContract(ctx context.Context, c TaskContract) context.Context {
	return context.WithValue(ctx, taskContractContextKey, c)
}
