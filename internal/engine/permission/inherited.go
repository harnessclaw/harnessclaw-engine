package permission

import (
	"context"
	"encoding/json"
	"sync"
)

// InheritedChecker inherits permissions from a parent session.
// - Read-only tools: auto-allow
// - Write tools that parent approved: allow
// - Write tools not approved by parent: Ask — the executor bubbles the
//   request to the root session's UI (sub-agents have no UI of their own).
type InheritedChecker struct {
	mu       sync.RWMutex
	approved map[string]bool // tool names approved by parent session
}

// NewInheritedChecker creates a checker seeded with the given approved tool names.
func NewInheritedChecker(approvedTools []string) *InheritedChecker {
	m := make(map[string]bool, len(approvedTools))
	for _, t := range approvedTools {
		m[t] = true
	}
	return &InheritedChecker{approved: m}
}

// Check evaluates whether a tool invocation is permitted based on inherited
// parent-session approvals. Read-only tools are always allowed. Write tools
// are allowed if previously approved by the parent; otherwise the decision
// is Ask so the executor can bubble the request to the root session's UI.
func (ic *InheritedChecker) Check(_ context.Context, toolName string, _ json.RawMessage, isReadOnly bool) *Result {
	if isReadOnly {
		return &Result{Decision: Allow, Reason: ReasonReadOnly}
	}
	ic.mu.RLock()
	defer ic.mu.RUnlock()
	if ic.approved[toolName] {
		return &Result{Decision: Allow, Reason: ReasonRule, Message: "inherited from parent session"}
	}
	return &Result{Decision: Ask, Reason: ReasonDefault, Message: "sub-agent: requires user approval"}
}

// Approve adds a tool to the approved set (thread-safe).
func (ic *InheritedChecker) Approve(toolName string) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.approved[toolName] = true
}
