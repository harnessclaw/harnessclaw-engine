// Package session — Awaits state machine for session-scoped pending operations.
//
// Awaits replaces 4 cross-session maps that previously lived on
// QueryEngine. Per-session ownership simplifies abort semantics
// (Awaits.AbortAll closes every pending channel atomically) and
// removes a shared global mutex.
package session

import (
	"errors"
	"sync"
	"time"

	"harnessclaw-go/pkg/types"
)

// ErrAwaitNotFound is returned by Resolve* when the supplied ID does
// not match a pending await. Caller policy is to log and discard —
// the source of an unmatched response is usually a stale client retry.
var ErrAwaitNotFound = errors.New("await not found")

// ToolAwait represents one in-flight tool call awaiting a client result.
type ToolAwait struct {
	ToolUseID string
	ToolName  string
	Result    chan *types.ToolResultPayload
	CreatedAt time.Time
}

// PermAwait represents one in-flight permission request awaiting client approval.
type PermAwait struct {
	RequestID string
	Response  chan *types.PermissionResponse
	CreatedAt time.Time
}

// PlanAwait represents one in-flight plan approval request.
type PlanAwait struct {
	PlanID    string
	SessionID string
	Response  chan *types.PlanResponse
	CreatedAt time.Time
}

// StepDecisionAwait represents one in-flight step-decision request.
type StepDecisionAwait struct {
	RequestID string
	SessionID string
	Response  chan *types.StepDecisionResponse
	CreatedAt time.Time
}

// Awaits holds all session-scoped pending state machines: tool calls,
// permission requests, plan approvals, and step-decision prompts. Each
// kind has its own ID space and Push/Resolve pair, but all share a
// single mutex and AbortAll path so abort semantics are atomic.
type Awaits struct {
	mu            sync.Mutex
	tools         map[string]*ToolAwait
	perms         map[string]*PermAwait
	plans         map[string]*PlanAwait
	stepDecisions map[string]*StepDecisionAwait
}

// NewAwaits creates an empty Awaits.
func NewAwaits() *Awaits {
	return &Awaits{
		tools:         make(map[string]*ToolAwait),
		perms:         make(map[string]*PermAwait),
		plans:         make(map[string]*PlanAwait),
		stepDecisions: make(map[string]*StepDecisionAwait),
	}
}

// --- Tool ---

// PushTool registers a new ToolAwait and returns it. The caller reads
// the result from aw.Result; ResolveTool closes the channel.
func (a *Awaits) PushTool(useID, toolName string) *ToolAwait {
	aw := &ToolAwait{
		ToolUseID: useID,
		ToolName:  toolName,
		Result:    make(chan *types.ToolResultPayload, 1),
		CreatedAt: time.Now(),
	}
	a.mu.Lock()
	a.tools[useID] = aw
	a.mu.Unlock()
	return aw
}

// ResolveTool delivers the payload to the matching ToolAwait and
// removes it from the registry. Returns ErrAwaitNotFound if no await
// matches payload.ToolUseID.
func (a *Awaits) ResolveTool(payload *types.ToolResultPayload) error {
	a.mu.Lock()
	aw, ok := a.tools[payload.ToolUseID]
	if ok {
		delete(a.tools, payload.ToolUseID)
	}
	a.mu.Unlock()
	if !ok {
		return ErrAwaitNotFound
	}
	aw.Result <- payload
	close(aw.Result)
	return nil
}

// --- Perm ---

// PushPerm registers a new PermAwait and returns it.
func (a *Awaits) PushPerm(reqID string) *PermAwait {
	aw := &PermAwait{
		RequestID: reqID,
		Response:  make(chan *types.PermissionResponse, 1),
		CreatedAt: time.Now(),
	}
	a.mu.Lock()
	a.perms[reqID] = aw
	a.mu.Unlock()
	return aw
}

// ResolvePerm delivers the response to the matching PermAwait and
// removes it from the registry. Returns ErrAwaitNotFound if no await
// matches reqID.
func (a *Awaits) ResolvePerm(reqID string, resp *types.PermissionResponse) error {
	a.mu.Lock()
	aw, ok := a.perms[reqID]
	if ok {
		delete(a.perms, reqID)
	}
	a.mu.Unlock()
	if !ok {
		return ErrAwaitNotFound
	}
	aw.Response <- resp
	close(aw.Response)
	return nil
}

// --- Plan ---

// PushPlan registers a new PlanAwait and returns it.
func (a *Awaits) PushPlan(planID, sessionID string) *PlanAwait {
	aw := &PlanAwait{
		PlanID:    planID,
		SessionID: sessionID,
		Response:  make(chan *types.PlanResponse, 1),
		CreatedAt: time.Now(),
	}
	a.mu.Lock()
	a.plans[planID] = aw
	a.mu.Unlock()
	return aw
}

// ResolvePlan delivers the response to the matching PlanAwait and
// removes it from the registry. Returns ErrAwaitNotFound if no await
// matches planID.
func (a *Awaits) ResolvePlan(planID string, resp *types.PlanResponse) error {
	a.mu.Lock()
	aw, ok := a.plans[planID]
	if ok {
		delete(a.plans, planID)
	}
	a.mu.Unlock()
	if !ok {
		return ErrAwaitNotFound
	}
	aw.Response <- resp
	close(aw.Response)
	return nil
}

// --- StepDecision ---

// PushStepDecision registers a new StepDecisionAwait and returns it.
func (a *Awaits) PushStepDecision(reqID, sessionID string) *StepDecisionAwait {
	aw := &StepDecisionAwait{
		RequestID: reqID,
		SessionID: sessionID,
		Response:  make(chan *types.StepDecisionResponse, 1),
		CreatedAt: time.Now(),
	}
	a.mu.Lock()
	a.stepDecisions[reqID] = aw
	a.mu.Unlock()
	return aw
}

// ResolveStepDecision delivers the response to the matching
// StepDecisionAwait and removes it from the registry. Returns
// ErrAwaitNotFound if no await matches reqID.
func (a *Awaits) ResolveStepDecision(reqID string, resp *types.StepDecisionResponse) error {
	a.mu.Lock()
	aw, ok := a.stepDecisions[reqID]
	if ok {
		delete(a.stepDecisions, reqID)
	}
	a.mu.Unlock()
	if !ok {
		return ErrAwaitNotFound
	}
	aw.Response <- resp
	close(aw.Response)
	return nil
}

// --- Abort ---

// AbortAll closes every pending channel in this Awaits and clears
// the maps. Waiters reading from any Result/Response channel unblock with
// the zero-value + closed signal. Reason is for logging only; the
// caller decides how to handle abort-vs-normal completion.
func (a *Awaits) AbortAll(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, aw := range a.tools {
		close(aw.Result)
		delete(a.tools, id)
	}
	for id, aw := range a.perms {
		close(aw.Response)
		delete(a.perms, id)
	}
	for id, aw := range a.plans {
		close(aw.Response)
		delete(a.plans, id)
	}
	for id, aw := range a.stepDecisions {
		close(aw.Response)
		delete(a.stepDecisions, id)
	}
}
