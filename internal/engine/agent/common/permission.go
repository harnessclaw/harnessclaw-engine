package common

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/loop/toolexec"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// BuildInheritedChecker returns a permission.Checker that inherits the
// parent session's user-approved tool whitelist. When approvedTools is
// empty, returns a BypassChecker (sub-agents auto-approve since they
// have no UI to surface approvals).
func BuildInheritedChecker(approvedTools []string) permission.Checker {
	if len(approvedTools) > 0 {
		return permission.NewInheritedChecker(approvedTools)
	}
	return permission.BypassChecker{}
}

// SessionApprovedTools returns the parent session's user-approved tool
// whitelist for inheritance into sub-agents. Returns nil when no
// session is found (caller treats nil as "no approvals to inherit",
// which BuildInheritedChecker maps to BypassChecker — legacy spawn
// behavior).
func SessionApprovedTools(mgr *session.Manager, parentSessionID string) []string {
	if mgr == nil || parentSessionID == "" {
		return nil
	}
	sess := mgr.Get(parentSessionID)
	if sess == nil {
		return nil
	}
	return sess.AllowedTools()
}

// BuildSubAgentApprovalFn returns a PermissionApprovalFunc for sub-agent
// loops that bubbles permission requests to the ROOT session: the pending
// await is registered on the root session (SubmitPermissionResult resolves
// there — the websocket conn is bound to the root), and the request event
// is emitted on the sub-agent's own out channel, which the dispatch relay
// forwards to the root UI. Returns nil when the root session can't be
// resolved (caller's executor then falls back to deny-on-Ask).
func BuildSubAgentApprovalFn(mgr *session.Manager, rootSessionID string, logger *zap.Logger) toolexec.PermissionApprovalFunc {
	if mgr == nil || rootSessionID == "" {
		return nil
	}
	rootSess := mgr.Get(rootSessionID)
	if rootSess == nil {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	return func(ctx context.Context, out chan<- types.EngineEvent, req *types.PermissionRequest) *types.PermissionResponse {
		permKey := req.PermissionKey
		if permKey == "" {
			permKey = req.ToolName
		}

		if rootSess.IsToolAllowed(permKey) {
			logger.Debug("sub-agent permission auto-approved (root session scope)",
				zap.String("permission_key", permKey),
				zap.String("root_session_id", rootSessionID),
			)
			return &types.PermissionResponse{
				RequestID: req.RequestID,
				Approved:  true,
				Scope:     types.PermissionScopeSession,
				Message:   "auto-approved (session scope)",
			}
		}

		aw := rootSess.Awaits.PushPerm(req.RequestID)
		defer rootSess.Awaits.ForgetPerm(req.RequestID)

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
		if resp == nil {
			// Channel closed without a value (Awaits.AbortAll) — treat as denial.
			return &types.PermissionResponse{
				RequestID: req.RequestID,
				Approved:  false,
				Message:   "request aborted",
			}
		}

		if resp.Approved && resp.Scope == types.PermissionScopeSession {
			rootSess.RememberAllowedTool(permKey)
			logger.Info("sub-agent command session-approved",
				zap.String("permission_key", permKey),
				zap.String("root_session_id", rootSessionID),
			)
		}

		return resp
	}
}
