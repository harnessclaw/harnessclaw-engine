package queryloop

import (
	"context"
	"fmt"

	"harnessclaw-go/pkg/types"
)

// RequestStepDecision pauses the coordinator on a hard step / plan
// failure and asks the user how to proceed. Mirrors RequestPlanApproval:
// emits a step_decision_requested event, registers the in-flight req
// keyed by request_id, blocks on either user response or ctx
// cancellation. Caller is expected to strip the inherited tool-ctx
// deadline (context.WithoutCancel) so the user gets unbounded time —
// same policy as plan_review and ask_user_question.
func (r *Runner) RequestStepDecision(
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

	sess := r.deps.SessionMgr().Get(sessionID)
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
