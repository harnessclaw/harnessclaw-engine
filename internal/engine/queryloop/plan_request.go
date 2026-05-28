package queryloop

import (
	"context"
	"fmt"

	"harnessclaw-go/pkg/types"
)

// RequestPlanApproval registers a pending plan request, emits the
// proposal event so the channel can forward it, and blocks until the
// client sends back a plan.response (or ctx is cancelled).
//
// Returns:
//   - resp: client's response (Approved + optional UpdatedSteps)
//   - err:  ctx cancellation, or "client never responded" timeout
//
// Used by PlanCoordinator. The function is non-method-style
// (`func (qe *QueryEngine)`) so future implementations of the
// approval mechanism (HTTP callback, kafka, etc.) can swap in via
// dependency injection without changing the call site.
func (r *Runner) RequestPlanApproval(
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

	sess := r.deps.SessionMgr().Get(sessionID)
	if sess == nil {
		return nil, fmt.Errorf("session %s not found for plan approval", sessionID)
	}
	aw := sess.Awaits.PushPlan(proposal.PlanID, sessionID)
	defer sess.Awaits.ForgetPlan(proposal.PlanID)

	// Emit the proposal event. The router/channel forwards to the
	// client. If the channel doesn't exist (tests), we still register
	// the pending request and let the caller drive SubmitPlanResponse
	// directly.
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

	// Block on response or context cancellation.
	select {
	case resp := <-aw.Response:
		// Echo "approved" event so the client can match its own
		// confirmation cycle and the trace shows the round-trip.
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
