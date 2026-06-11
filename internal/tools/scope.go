package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"harnessclaw-go/pkg/types"
)

// EnforceReadScope verifies that path is within the agent's ReadScope.
// When the path is outside scope, the ctx-injected ScopeEscalationFn is
// consulted — fn returning true grants access (e.g. user approved a
// permission card). When no fn is wired or the user declines, the call
// returns a permission_denied ToolResult that the caller must surface.
//
// Returns nil when enforcement is not needed (no AgentScope on ctx, empty
// ReadScope means "no restriction", in-scope path, or escalation approved).
func EnforceReadScope(ctx context.Context, path string) *types.ToolResult {
	return enforceScope(ctx, path, true)
}

// EnforceWriteScope is the write-side counterpart of EnforceReadScope.
func EnforceWriteScope(ctx context.Context, path string) *types.ToolResult {
	return enforceScope(ctx, path, false)
}

func enforceScope(ctx context.Context, path string, isReadOnly bool) *types.ToolResult {
	scope, ok := AgentScopeFromCtx(ctx)
	if !ok {
		return nil
	}
	allowed := scope.WriteScope
	verb := "write"
	if isReadOnly {
		allowed = scope.ReadScope
		verb = "read"
	}
	if len(allowed) == 0 || pathInScope(path, allowed) {
		return nil
	}
	if fn, ok := ScopeEscalationFnFromCtx(ctx); ok && fn != nil && fn(ctx, path, isReadOnly) {
		return nil
	}
	return &types.ToolResult{
		Content:   fmt.Sprintf("path %q is outside the %s scope for this spawn (allowed prefixes: %v)", path, verb, allowed),
		IsError:   true,
		ErrorType: types.ToolErrorPermissionDenied,
	}
}

func pathInScope(path string, allowed []string) bool {
	p := filepath.Clean(path)
	for _, a := range allowed {
		ac := filepath.Clean(a)
		if p == ac || strings.HasPrefix(p, ac+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
