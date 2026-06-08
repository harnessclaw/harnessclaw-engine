package permission

import (
	"context"
	"encoding/json"
)

// PipelineStep is a single step in the permission pipeline.
// Returns nil to indicate "passthrough" (no decision — let the next step decide).
type PipelineStep interface {
	Check(ctx context.Context, req *PermissionRequest) *Result
}

// PermissionRequest bundles all inputs for a permission check.
type PermissionRequest struct {
	ToolName   string
	Input      json.RawMessage
	IsReadOnly bool
	// Tool is the tool.Tool instance (as interface{} to avoid circular import).
	// Steps that need tool-specific checks can type-assert to tool.PermissionPreChecker.
	Tool interface{}
	// Mode is the active permission mode.
	Mode Mode
	// Rules are the active permission rules.
	Rules []Rule
}

// ---------------------------------------------------------------------------
// Pipeline step implementations — mirrors the TypeScript 7-step inner pipeline
// from src/hooks/toolPermission/permissions.ts
// ---------------------------------------------------------------------------

// DenyRuleStep checks global deny rules. (Step 1a)
// If any deny rule matches, the tool is denied immediately.
type DenyRuleStep struct{}

func (s *DenyRuleStep) Check(_ context.Context, req *PermissionRequest) *Result {
	for _, bucket := range bySourcePriority(req.Rules) {
		for _, rule := range bucket {
			if rule.Behavior == BehaviorDeny && matchesToolName(rule.ToolName, req.ToolName) {
				return &Result{
					Decision: Deny,
					Reason:   ReasonRule,
					Message:  "denied by rule: " + rule.ToolName,
				}
			}
		}
	}
	return nil // passthrough
}

// ToolCheckPermStep calls the tool's own CheckPermission if implemented. (Step 1c)
// This allows tools to provide domain-specific permission logic.
type ToolCheckPermStep struct{}

func (s *ToolCheckPermStep) Check(ctx context.Context, req *PermissionRequest) *Result {
	if req.Tool == nil {
		return nil
	}

	// Check if tool implements PermissionPreChecker via its own interface.
	type permPreChecker interface {
		CheckPermission(ctx context.Context, input json.RawMessage) interface{}
	}
	checker, ok := req.Tool.(permPreChecker)
	if !ok {
		return nil
	}

	preResult := checker.CheckPermission(ctx, req.Input)
	if preResult == nil {
		return nil
	}

	// Try to extract behavior from the result.
	type behaviorResult interface {
		GetBehavior() string
		GetMessage() string
	}
	if br, ok := preResult.(behaviorResult); ok {
		switch br.GetBehavior() {
		case "allow":
			return &Result{Decision: Allow, Reason: ReasonHook, Message: br.GetMessage()}
		case "deny":
			return &Result{Decision: Deny, Reason: ReasonHook, Message: br.GetMessage()}
		case "ask":
			return &Result{Decision: Ask, Reason: ReasonHook, Message: br.GetMessage()}
		}
	}
	return nil // passthrough
}

// BypassModeStep handles bypass mode. (Step 2a)
// In bypass mode, everything is allowed.
type BypassModeStep struct{}

func (s *BypassModeStep) Check(_ context.Context, req *PermissionRequest) *Result {
	if req.Mode == ModeBypass {
		return &Result{Decision: Allow, Reason: ReasonBypass}
	}
	return nil
}

// AlwaysAllowRuleStep checks tool-level always-allow rules. (Step 2b)
type AlwaysAllowRuleStep struct{}

func (s *AlwaysAllowRuleStep) Check(_ context.Context, req *PermissionRequest) *Result {
	for _, bucket := range bySourcePriority(req.Rules) {
		for _, rule := range bucket {
			if rule.Behavior == BehaviorAllow && matchesToolName(rule.ToolName, req.ToolName) {
				return &Result{Decision: Allow, Reason: ReasonRule}
			}
		}
	}
	return nil
}

// ReadOnlyAutoAllowStep auto-allows read-only tools in most modes.
type ReadOnlyAutoAllowStep struct{}

func (s *ReadOnlyAutoAllowStep) Check(_ context.Context, req *PermissionRequest) *Result {
	if req.IsReadOnly && req.Mode != ModeBypass {
		return &Result{Decision: Allow, Reason: ReasonReadOnly}
	}
	return nil
}

// ModeDefaultStep applies mode-specific defaults as the final step. (Step 3)
type ModeDefaultStep struct{}

func (s *ModeDefaultStep) Check(_ context.Context, req *PermissionRequest) *Result {
	switch req.Mode {
	case ModePlan:
		if req.IsReadOnly {
			return &Result{Decision: Allow, Reason: ReasonReadOnly}
		}
		return &Result{Decision: Ask, Reason: ReasonMode, Message: "plan mode: write operation requires confirmation"}

	case ModeAcceptEdits:
		if req.IsReadOnly || isFileEditTool(req.ToolName) {
			return &Result{Decision: Allow, Reason: ReasonMode}
		}
		return &Result{Decision: Ask, Reason: ReasonMode, Message: "non-edit write operation requires confirmation"}

	case ModeDontAsk:
		return &Result{Decision: Allow, Reason: ReasonMode}

	case ModeAuto:
		// Auto mode: for now, treat write operations as Ask.
		// Future: integrate LLM classifier for automatic decisions.
		if req.IsReadOnly {
			return &Result{Decision: Allow, Reason: ReasonReadOnly}
		}
		return &Result{Decision: Ask, Reason: ReasonMode, Message: "auto mode: write operation awaiting classification"}

	default: // ModeDefault
		if req.IsReadOnly {
			return &Result{Decision: Allow, Reason: ReasonReadOnly}
		}
		return &Result{Decision: Ask, Reason: ReasonDefault, Message: "write operation requires confirmation"}
	}
}

// matchesToolName checks if a rule's tool name matches the target tool.
func matchesToolName(ruleToolName, targetToolName string) bool {
	return ruleToolName == "*" || ruleToolName == targetToolName
}
