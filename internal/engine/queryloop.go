package engine

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/pkg/types"
)

// requestPermissionApproval registers a pending permission request, emits the
// event to the client, and blocks until the client responds or the context is cancelled.
// This is passed to the ToolExecutor as a callback.
//
// If the tool+command has been previously approved with scope=session for this session,
// the request is auto-approved without asking the client again.
func (qe *QueryEngine) requestPermissionApproval(
	ctx context.Context,
	out chan<- types.EngineEvent,
	sessionID string,
	req *types.PermissionRequest,
) *types.PermissionResponse {
	permKey := req.PermissionKey
	if permKey == "" {
		permKey = req.ToolName // fallback for non-Bash tools without a specific key
	}

	// Fast path: check if this tool+command is already session-approved.
	sess := qe.sessionMgr.Get(sessionID)
	if sess != nil && sess.IsToolAllowed(permKey) {
		qe.logger.Debug("permission auto-approved (session scope)",
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
		qe.logger.Warn("requestPermissionApproval: session not found",
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

	// Emit permission_request event to the client.
	out <- types.EngineEvent{
		Type:              types.EngineEventPermissionRequest,
		PermissionRequest: req,
	}

	// Wait for client response — block indefinitely until the user acts or
	// the session is aborted (ctx cancelled).  Permission decisions are a
	// human action; applying an artificial timeout would silently deny
	// operations the user simply hasn't reviewed yet.
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

	// If approved with session scope, record for future auto-approval.
	if resp.Approved && resp.Scope == types.PermissionScopeSession {
		if sess != nil {
			sess.RememberAllowedTool(permKey)
		}
		qe.logger.Info("command session-approved",
			zap.String("permission_key", permKey),
			zap.String("session_id", sessionID),
		)
	}

	return resp
}

// getSkillListingFiltered delegates to the queryloop.Runner. Kept on the
// engine-side facade because spawner_facade.go (spawn.Deps implementation)
// reaches it from outside the queryloop package.
func (qe *QueryEngine) getSkillListingFiltered(allowedSkills map[string]bool) string {
	return qe.loopRunner.GetSkillListingFiltered(allowedSkills)
}

// getEnvSnapshot delegates to the queryloop.Runner. Spawn-side Deps
// (spawner_facade.go's GetEnvSnapshot) reaches the same code through this
// wrapper, so behaviour stays identical.
func (qe *QueryEngine) getEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	return qe.loopRunner.GetEnvSnapshot(sessionRoot)
}

