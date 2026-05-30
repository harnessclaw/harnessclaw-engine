package common

import (
	"harnessclaw-go/internal/permission"
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
