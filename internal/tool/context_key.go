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

// CurrentImage is an image supplied by the current user turn. The router
// attaches these to the tool execution context so image-generation tools can
// use uploaded reference images without asking the model to echo base64 in
// tool arguments.
type CurrentImage struct {
	MediaType string
	Data      string
	URL       string
	Path      string
	Filename  string
	Size      int64
}

type currentImagesKey struct{}

var currentImagesContextKey = currentImagesKey{}

// CurrentImagesFromCtx returns the current turn's image inputs, when present.
func CurrentImagesFromCtx(ctx context.Context) ([]CurrentImage, bool) {
	images, ok := ctx.Value(currentImagesContextKey).([]CurrentImage)
	return images, ok
}

// WithCurrentImages attaches current-turn image inputs to ctx.
func WithCurrentImages(ctx context.Context, images []CurrentImage) context.Context {
	if len(images) == 0 {
		return ctx
	}
	copied := append([]CurrentImage(nil), images...)
	return context.WithValue(ctx, currentImagesContextKey, copied)
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

// TaskContract is the deliverable contract attached to a sub-agent's
// dispatch. The framework injects it via ctx so submit_task_result can
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
	// OutputSchema is the per-agent declared structured-result shape
	// (mirrors AgentDefinition.OutputSchema for TierSubAgent). When an
	// agent submits a direct `result` payload, submit_task_result validates
	// it server-side against this schema. Empty means no structured-result
	// schema is available.
	OutputSchema map[string]any
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

// coordinatorModeKey carries the operator-supplied L2 coordinator mode
// preference (e.g. from a WebSocket session parameter) down to the
// scheduler tool, which threads it onto SpawnConfig.CoordinatorMode.
//
// Mode is intentionally NOT exposed in emma's tool input — emma should
// not have to choose between react and plan; that's an operator / API
// surface decision. emma always calls scheduler with the task; the
// runtime decides which coordinator backs the call based on this ctx
// value (defaults to "" → react via registry fallback).
type coordinatorModeKey struct{}

var coordinatorModeContextKey = coordinatorModeKey{}

// GetCoordinatorMode returns the L2 coordinator mode preference attached
// to ctx, or "" when absent (which the engine resolves to ReAct).
func GetCoordinatorMode(ctx context.Context) string {
	if v, ok := ctx.Value(coordinatorModeContextKey).(string); ok {
		return v
	}
	return ""
}

// WithCoordinatorMode attaches a coordinator mode preference to ctx. The
// API / WebSocket layer calls this when forwarding a session-level mode
// parameter, e.g. when an operator opts in to plan mode for a debug run.
func WithCoordinatorMode(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, coordinatorModeContextKey, mode)
}

// planConfirmationKey carries the per-turn opt-in for plan-mode user
// confirmation. Allowed values: "" / "auto" (no pause), "required"
// (PlanCoordinator emits plan.proposed and waits). Threading via ctx
// avoids dragging the field through every coordinator interface.
type planConfirmationKey struct{}

var planConfirmationContextKey = planConfirmationKey{}

// GetPlanConfirmation returns the plan confirmation mode attached to ctx,
// or "" when absent (treated as "auto"). The PlanCoordinator reads it at
// the start of Run.
func GetPlanConfirmation(ctx context.Context) string {
	if v, ok := ctx.Value(planConfirmationContextKey).(string); ok {
		return v
	}
	return ""
}

// WithPlanConfirmation attaches a plan confirmation mode to ctx.
func WithPlanConfirmation(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, planConfirmationContextKey, mode)
}

// AgentScope is the per-spawn filesystem scope. The engine sets it just
// before tool Execute so File* tools can reject paths outside the scope.
// Empty/nil scopes mean "no restriction" — preserves backward compat for
// callers that haven't migrated yet (legacy tests, ad-hoc spawn).
type AgentScope struct {
	// ReadScope lists absolute path prefixes a tool may read from.
	ReadScope []string
	// WriteScope lists absolute path prefixes a tool may write to.
	WriteScope []string
	// SessionRoot is the {workspace}/session/{root-session-id} dir for
	// this spawn. Tools may use it to derive relative paths for logging.
	SessionRoot string
	// TaskID is the plan-step id this spawn was dispatched for. Tools
	// like meta_write read it from ctx instead of trusting an LLM-supplied
	// value (which the model has been observed to confuse with session_id).
	TaskID string
	// Agent is the subagent_type for this spawn (e.g. "freelancer"). Same
	// rationale as TaskID — framework-known fields shouldn't be LLM input.
	Agent string
}

type agentScopeKey struct{}

var agentScopeContextKey = agentScopeKey{}

// WithAgentScope attaches a scope to ctx.
func WithAgentScope(ctx context.Context, s AgentScope) context.Context {
	return context.WithValue(ctx, agentScopeContextKey, s)
}

// AgentScopeFromCtx returns the scope and ok=false when absent.
func AgentScopeFromCtx(ctx context.Context) (AgentScope, bool) {
	s, ok := ctx.Value(agentScopeContextKey).(AgentScope)
	return s, ok
}

// ScopeEscalationFn is called by file tools when a path is outside the
// current read scope. It presents a permission prompt to the user and
// returns true if access was granted.
type ScopeEscalationFn func(ctx context.Context, path string, isReadOnly bool) bool

type scopeEscalationKey struct{}

var scopeEscalationContextKey = scopeEscalationKey{}

// WithScopeEscalationFn attaches a scope escalation function to ctx.
func WithScopeEscalationFn(ctx context.Context, fn ScopeEscalationFn) context.Context {
	return context.WithValue(ctx, scopeEscalationContextKey, fn)
}

// ScopeEscalationFnFromCtx retrieves the scope escalation function from ctx.
// Returns nil, false when not present (legacy tools, tests).
func ScopeEscalationFnFromCtx(ctx context.Context) (ScopeEscalationFn, bool) {
	fn, ok := ctx.Value(scopeEscalationContextKey).(ScopeEscalationFn)
	return fn, ok
}
