package common_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/pkg/types"
)

func TestBuildInheritedChecker_NonEmptyApproved(t *testing.T) {
	chk := common.BuildInheritedChecker([]string{"bash", "write"})
	if chk == nil {
		t.Fatal("checker nil")
	}
}

func TestBuildInheritedChecker_EmptyApproved_ReturnsBypass(t *testing.T) {
	chk := common.BuildInheritedChecker(nil)
	if chk == nil {
		t.Fatal("checker nil")
	}
}

// newRootSession builds a session manager plus a root session for the
// approval-fn tests.
func newRootSession(t *testing.T) (*session.Manager, *session.Session) {
	t.Helper()
	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	sess, err := mgr.GetOrCreate(context.Background(), "root_sess", "test", "user1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	return mgr, sess
}

func TestBuildSubAgentApprovalFn_BubblesAndRemembersSessionScope(t *testing.T) {
	mgr, rootSess := newRootSession(t)

	fn := common.BuildSubAgentApprovalFn(mgr, rootSess.ID, zap.NewNop())
	if fn == nil {
		t.Fatal("approval fn nil for resolvable root session")
	}

	out := make(chan types.EngineEvent, 4)
	req := &types.PermissionRequest{
		RequestID:     "perm_x",
		ToolName:      "write",
		PermissionKey: "write",
	}

	done := make(chan *types.PermissionResponse, 1)
	go func() {
		done <- fn(context.Background(), out, req)
	}()

	// The request event must appear on the sub-agent's out channel.
	select {
	case evt := <-out:
		if evt.Type != types.EngineEventPermissionRequest {
			t.Fatalf("event type = %q, want %q", evt.Type, types.EngineEventPermissionRequest)
		}
		if evt.PermissionRequest == nil || evt.PermissionRequest.RequestID != "perm_x" {
			t.Fatalf("event PermissionRequest = %+v, want RequestID perm_x", evt.PermissionRequest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission_request event")
	}

	// Resolve on the ROOT session — mirrors SubmitPermissionResult.
	err := rootSess.Awaits.ResolvePerm("perm_x", &types.PermissionResponse{
		RequestID: "perm_x",
		Approved:  true,
		Scope:     types.PermissionScopeSession,
	})
	if err != nil {
		t.Fatalf("ResolvePerm: %v", err)
	}

	select {
	case resp := <-done:
		if resp == nil || !resp.Approved {
			t.Fatalf("response = %+v, want Approved=true", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval fn to return")
	}

	if !rootSess.IsToolAllowed("write") {
		t.Error("session-scope approval should be remembered on the root session")
	}
}

func TestBuildSubAgentApprovalFn_CtxCancelDeniesAndForgets(t *testing.T) {
	mgr, rootSess := newRootSession(t)
	fn := common.BuildSubAgentApprovalFn(mgr, rootSess.ID, zap.NewNop())
	if fn == nil {
		t.Fatal("approval fn nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan types.EngineEvent, 4)
	req := &types.PermissionRequest{
		RequestID:     "perm_cancel",
		ToolName:      "bash",
		PermissionKey: "Bash:rm",
	}

	done := make(chan *types.PermissionResponse, 1)
	go func() {
		done <- fn(ctx, out, req)
	}()

	// Wait for the bubble event, then cancel instead of answering.
	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission_request event")
	}
	cancel()

	select {
	case resp := <-done:
		if resp == nil || resp.Approved {
			t.Fatalf("response = %+v, want denied on ctx cancel", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval fn to return after cancel")
	}

	// The await must be forgotten — a late resolve finds nothing.
	err := rootSess.Awaits.ResolvePerm("perm_cancel", &types.PermissionResponse{
		RequestID: "perm_cancel",
		Approved:  true,
	})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("late ResolvePerm err = %v, want ErrAwaitNotFound (await leaked)", err)
	}
}

func TestBuildSubAgentApprovalFn_PreAllowedAutoApprovesWithoutEmitting(t *testing.T) {
	mgr, rootSess := newRootSession(t)
	rootSess.RememberAllowedTool("write")

	fn := common.BuildSubAgentApprovalFn(mgr, rootSess.ID, zap.NewNop())
	if fn == nil {
		t.Fatal("approval fn nil")
	}

	out := make(chan types.EngineEvent, 4)
	resp := fn(context.Background(), out, &types.PermissionRequest{
		RequestID:     "perm_pre",
		ToolName:      "write",
		PermissionKey: "write",
	})
	if resp == nil || !resp.Approved {
		t.Fatalf("response = %+v, want auto-approved", resp)
	}
	if len(out) != 0 {
		t.Errorf("expected no event emitted for pre-allowed tool, got %d", len(out))
	}
}

func TestBuildSubAgentApprovalFn_UnresolvableRootReturnsNil(t *testing.T) {
	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	if fn := common.BuildSubAgentApprovalFn(mgr, "no_such_session", zap.NewNop()); fn != nil {
		t.Error("expected nil fn for unknown root session")
	}
	if fn := common.BuildSubAgentApprovalFn(nil, "root", zap.NewNop()); fn != nil {
		t.Error("expected nil fn for nil manager")
	}
	if fn := common.BuildSubAgentApprovalFn(mgr, "", zap.NewNop()); fn != nil {
		t.Error("expected nil fn for empty root session id")
	}
}
