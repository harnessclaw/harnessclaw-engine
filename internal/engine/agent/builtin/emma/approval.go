package emma

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

// requestPlanApproval registers a pending plan request, emits the proposal
// event so the channel can forward it, and blocks until the client sends
// back a plan.response (or ctx is cancelled). Used by PlanCoordinator.
func (e *Engine) requestPlanApproval(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	proposal *types.PlanProposal,
) (*types.PlanResponse, error) {
	if proposal == nil {
		return nil, fmt.Errorf("plan approval: nil proposal")
	}
	if proposal.PlanID == "" {
		return nil, fmt.Errorf("plan approval: empty plan_id")
	}

	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return nil, fmt.Errorf("session %s not found for plan approval", sessionID)
	}
	aw := sess.Awaits.PushPlan(proposal.PlanID, sessionID)
	defer sess.Awaits.ForgetPlan(proposal.PlanID)

	if out != nil {
		select {
		case out <- types.EngineEvent{
			Type:         types.EngineEventPlanProposed,
			AgentID:      proposal.AgentID,
			PlanProposal: proposal,
		}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case resp := <-aw.Response:
		if out != nil && resp != nil {
			out <- types.EngineEvent{
				Type:         types.EngineEventPlanApproved,
				AgentID:      proposal.AgentID,
				PlanProposal: proposal,
			}
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// requestStepDecision pauses the coordinator on a hard step / plan failure
// and asks the user how to proceed. Mirrors requestPlanApproval.
func (e *Engine) requestStepDecision(
	ctx context.Context,
	sessionID string,
	out chan<- types.EngineEvent,
	req *types.StepDecisionRequest,
) (*types.StepDecisionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("step decision: nil request")
	}
	if req.RequestID == "" {
		return nil, fmt.Errorf("step decision: empty request_id")
	}

	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return nil, fmt.Errorf("session %s not found for step decision", sessionID)
	}
	aw := sess.Awaits.PushStepDecision(req.RequestID, sessionID)
	defer sess.Awaits.ForgetStepDecision(req.RequestID)

	if out != nil {
		select {
		case out <- types.EngineEvent{
			Type:         types.EngineEventStepDecisionRequested,
			AgentID:      req.AgentID,
			StepDecision: req,
		}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case resp := <-aw.Response:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// requestPermissionApproval registers a pending permission request, emits
// the event to the client, and blocks until the client responds or the
// context is cancelled. Passed to the ToolExecutor as a callback.
//
// If the tool+command has been previously approved with scope=session for
// this session, the request is auto-approved without asking the client
// again.
func (e *Engine) requestPermissionApproval(
	ctx context.Context,
	out chan<- types.EngineEvent,
	sessionID string,
	req *types.PermissionRequest,
) *types.PermissionResponse {
	permKey := req.PermissionKey
	if permKey == "" {
		permKey = req.ToolName
	}

	sess := e.sessionMgr.Get(sessionID)
	if sess != nil && sess.IsToolAllowed(permKey) {
		e.logger.Debug("permission auto-approved (session scope)",
			zap.String("permission_key", permKey),
			zap.String("session_id", sessionID),
		)
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  true,
			Scope:     types.PermissionScopeSession,
			Message:   "auto-approved (session scope)",
		}
	}
	if sess == nil {
		e.logger.Warn("requestPermissionApproval: session not found",
			zap.String("session_id", sessionID),
		)
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  false,
			Message:   "session expired",
		}
	}

	aw := sess.Awaits.PushPerm(req.RequestID)
	defer sess.Awaits.ForgetPerm(req.RequestID)

	out <- types.EngineEvent{
		Type:              types.EngineEventPermissionRequest,
		PermissionRequest: req,
	}

	var resp *types.PermissionResponse
	select {
	case <-ctx.Done():
		return &types.PermissionResponse{
			RequestID: req.RequestID,
			Approved:  false,
			Message:   "request cancelled",
		}
	case resp = <-aw.Response:
	}

	if resp.Approved && resp.Scope == types.PermissionScopeSession {
		if sess != nil {
			sess.RememberAllowedTool(permKey)
		}
		e.logger.Info("command session-approved",
			zap.String("permission_key", permKey),
			zap.String("session_id", sessionID),
		)
	}

	return resp
}
