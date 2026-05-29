package emma

import (
	"context"
	"fmt"

	"harnessclaw-go/pkg/types"
)

// SubmitToolResult implements engine.Engine. Delivers a client-side tool
// execution result back to the engine so the query loop can continue with
// the next LLM turn. Routes directly to sess.Awaits — no facade layer.
func (e *Engine) SubmitToolResult(_ context.Context, sessionID string, result *types.ToolResultPayload) error {
	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session lookup for tool result: session %s not found", sessionID)
	}
	if err := sess.Awaits.ResolveTool(result); err != nil {
		return fmt.Errorf("no pending tool call for tool_use_id %s: %w", result.ToolUseID, err)
	}
	return nil
}

// SubmitPermissionResult implements engine.Engine. Delivers a permission
// approval/denial decision from the client to the waiting tool executor
// via sess.Awaits.
func (e *Engine) SubmitPermissionResult(_ context.Context, sessionID string, resp *types.PermissionResponse) error {
	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session lookup for permission result: session %s not found", sessionID)
	}
	if err := sess.Awaits.ResolvePerm(resp.RequestID, resp); err != nil {
		return fmt.Errorf("no pending permission request for request_id %s: %w", resp.RequestID, err)
	}
	return nil
}

// SubmitPlanResponse implements engine.Engine. Routes the user's
// plan.response back to the awaiting requestPlanApproval call via
// sess.Awaits.
func (e *Engine) SubmitPlanResponse(_ context.Context, sessionID string, resp *types.PlanResponse) error {
	if resp == nil || resp.PlanID == "" {
		return fmt.Errorf("plan response: missing plan_id")
	}
	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session %s not found for plan response", sessionID)
	}
	if err := sess.Awaits.ResolvePlan(resp.PlanID, resp); err != nil {
		return fmt.Errorf("no pending plan request for plan_id %s: %w", resp.PlanID, err)
	}
	return nil
}

// SubmitStepDecision implements engine.Engine. Routes the user's
// step-decision reply back to the coordinator blocked in
// requestStepDecision via sess.Awaits.
func (e *Engine) SubmitStepDecision(_ context.Context, sessionID string, resp *types.StepDecisionResponse) error {
	if resp == nil || resp.RequestID == "" {
		return fmt.Errorf("step decision response: missing request_id")
	}
	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return fmt.Errorf("session %s not found for step decision", sessionID)
	}
	if err := sess.Awaits.ResolveStepDecision(resp.RequestID, resp); err != nil {
		return fmt.Errorf("no pending step decision for request_id %s: %w", resp.RequestID, err)
	}
	return nil
}
