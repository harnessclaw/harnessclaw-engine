package permission

import (
	"context"
	"encoding/json"
	"strings"
)

// Checker evaluates whether a tool invocation is permitted.
type Checker interface {
	Check(ctx context.Context, toolName string, input json.RawMessage, isReadOnly bool) *Result
}

// DefaultChecker resolves permissions using the active Mode and a set of Rules.
type DefaultChecker struct {
	mode  Mode
	rules []Rule
}

// NewChecker creates a permission checker.
func NewChecker(mode Mode, rules []Rule) *DefaultChecker {
	return &DefaultChecker{mode: mode, rules: rules}
}

// Check evaluates a tool call against rules and mode.
//
// Evaluation order:
//  1. Bypass mode → allow immediately
//  2. Explicit rules (highest-priority source first) → allow or deny
//  3. Mode-specific logic (plan, accept_edits, default, etc.)
func (c *DefaultChecker) Check(_ context.Context, toolName string, _ json.RawMessage, isReadOnly bool) *Result {
	// 1. Bypass — everything is allowed.
	if c.mode == ModeBypass {
		return &Result{Decision: Allow, Reason: ReasonBypass}
	}

	// 2. Explicit rules, evaluated by source priority.
	if r := c.matchRule(toolName); r != nil {
		return r
	}

	// 3. Mode-based default.
	switch c.mode {
	case ModePlan:
		if isReadOnly {
			return &Result{Decision: Allow, Reason: ReasonReadOnly}
		}
		return &Result{Decision: Ask, Reason: ReasonMode, Message: "plan mode: write operation requires confirmation"}

	case ModeAcceptEdits:
		if isReadOnly || isFileEditTool(toolName) {
			return &Result{Decision: Allow, Reason: ReasonMode}
		}
		return &Result{Decision: Ask, Reason: ReasonMode, Message: "non-edit write operation requires confirmation"}

	case ModeDontAsk:
		return &Result{Decision: Allow, Reason: ReasonMode}

	default: // ModeDefault, ModeAuto
		if isReadOnly {
			return &Result{Decision: Allow, Reason: ReasonReadOnly}
		}
		return &Result{Decision: Ask, Reason: ReasonDefault, Message: "write operation requires confirmation"}
	}
}

// matchRule returns the first matching rule result, or nil.
func (c *DefaultChecker) matchRule(toolName string) *Result {
	for _, bucket := range bySourcePriority(c.rules) {
		for _, rule := range bucket {
			if rule.ToolName == "*" || rule.ToolName == toolName {
				d := Allow
				if rule.Behavior == BehaviorDeny {
					d = Deny
				}
				return &Result{Decision: d, Reason: ReasonRule}
			}
		}
	}
	return nil
}

// isFileEditTool returns true for tools that modify files.
func isFileEditTool(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "edit") ||
		strings.Contains(lower, "write") ||
		lower == "fileedit" || lower == "filewrite" ||
		lower == "file_edit" || lower == "file_write"
}

// BypassChecker always allows every tool call.
type BypassChecker struct{}

func (BypassChecker) Check(_ context.Context, _ string, _ json.RawMessage, _ bool) *Result {
	return &Result{Decision: Allow, Reason: ReasonBypass}
}
