package common

import (
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/permission"
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
