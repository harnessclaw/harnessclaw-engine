package tool

import (
	"context"
	"encoding/json"

	"harnessclaw-go/internal/permission"
)

// PermissionChecker determines whether a tool call is permitted.
// It wraps the permission.Checker interface at the tool layer.
type PermissionChecker interface {
	// Check evaluates whether the given tool call is allowed.
	Check(ctx context.Context, toolName string, input json.RawMessage, isReadOnly bool) *permission.Result
}

// AllowAllChecker always permits tool execution (bypass mode).
type AllowAllChecker struct{}

func (AllowAllChecker) Check(_ context.Context, _ string, _ json.RawMessage, _ bool) *permission.Result {
	return &permission.Result{Decision: permission.Allow, Reason: permission.ReasonBypass}
}
