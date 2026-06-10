package emma

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

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
