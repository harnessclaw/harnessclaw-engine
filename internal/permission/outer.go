package permission

import (
	"context"
	"encoding/json"
)

// PipelineChecker implements the full permission pipeline using composable steps.
// It replaces the monolithic check logic with individually testable steps.
type PipelineChecker struct {
	mode  Mode
	rules []Rule
	steps []PipelineStep
}

// NewPipelineChecker creates a pipeline checker with the standard step sequence.
// The step order mirrors the TypeScript hasPermissionsToUseToolInner() pipeline:
//   1. Deny rules (hard deny)
//   2. Tool-specific permission pre-check
//   3. Bypass mode (allow all)
//   4. Allow rules (explicit allow)
//   5. Read-only auto-allow
//   6. Mode-based defaults (fallback)
func NewPipelineChecker(mode Mode, rules []Rule) *PipelineChecker {
	return &PipelineChecker{
		mode:  mode,
		rules: rules,
		steps: []PipelineStep{
			&DenyRuleStep{},         // Step 1a: deny rules
			&ToolCheckPermStep{},    // Step 1c: tool-specific pre-check
			&BypassModeStep{},       // Step 2a: bypass mode
			&AlwaysAllowRuleStep{},  // Step 2b: allow rules
			&ReadOnlyAutoAllowStep{}, // auto-allow read-only
			&ModeDefaultStep{},      // Step 3: mode-based default
		},
	}
}

// Check evaluates a tool call through the pipeline steps in order.
// The first step to return a non-nil Result wins.
func (pc *PipelineChecker) Check(ctx context.Context, toolName string, input json.RawMessage, isReadOnly bool) *Result {
	req := &PermissionRequest{
		ToolName:   toolName,
		Input:      input,
		IsReadOnly: isReadOnly,
		Mode:       pc.mode,
		Rules:      pc.rules,
	}
	return pc.CheckWithTool(ctx, req)
}

// CheckWithTool evaluates a pre-built PermissionRequest through the pipeline.
// This variant allows passing the Tool instance for tool-specific checks.
func (pc *PipelineChecker) CheckWithTool(ctx context.Context, req *PermissionRequest) *Result {
	// Ensure mode and rules are set from the checker's config.
	req.Mode = pc.mode
	req.Rules = pc.rules

	for _, step := range pc.steps {
		if r := step.Check(ctx, req); r != nil {
			return r
		}
	}
	// Should never reach here — ModeDefaultStep always returns a result.
	return &Result{Decision: Ask, Reason: ReasonDefault}
}

// OuterChecker wraps PipelineChecker with post-processing logic.
// It handles:
//   - dontAsk mode conversion (Ask → Deny)
//   - auto-mode classifier (future)
//   - headless agent handling
//
// This mirrors the TypeScript hasPermissionsToUseTool() outer wrapper.
type OuterChecker struct {
	inner *PipelineChecker
	mode  Mode
}

// NewOuterChecker creates an outer checker wrapping a pipeline checker.
func NewOuterChecker(mode Mode, rules []Rule) *OuterChecker {
	return &OuterChecker{
		inner: NewPipelineChecker(mode, rules),
		mode:  mode,
	}
}

// Check evaluates a tool call with post-processing.
func (oc *OuterChecker) Check(ctx context.Context, toolName string, input json.RawMessage, isReadOnly bool) *Result {
	result := oc.inner.Check(ctx, toolName, input, isReadOnly)
	return oc.postProcess(result)
}

// postProcess applies outer-layer transformations to the inner result.
func (oc *OuterChecker) postProcess(result *Result) *Result {
	if result.Decision == Allow {
		return result
	}

	if result.Decision == Ask {
		// dontAsk mode: convert Ask → Deny.
		if oc.mode == ModeDontAsk {
			return &Result{
				Decision: Deny,
				Reason:   ReasonMode,
				Message:  "dontAsk mode rejects ask: " + result.Message,
			}
		}
		// Future: auto-mode classifier would go here.
		// It would analyze the tool call and decide Allow/Deny automatically.
	}

	return result
}
