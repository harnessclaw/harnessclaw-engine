package session_test

import (
	"errors"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

func TestAwaits_PushResolveTool(t *testing.T) {
	a := session.NewAwaits()
	aw := a.PushTool("use_1", "Read")
	if aw == nil {
		t.Fatal("PushTool returned nil")
	}

	payload := &types.ToolResultPayload{ToolUseID: "use_1", Status: "success", Output: "ok"}
	if err := a.ResolveTool(payload); err != nil {
		t.Fatalf("ResolveTool: %v", err)
	}

	select {
	case got := <-aw.Result:
		if got == nil || got.Output != "ok" {
			t.Errorf("got %#v, want ok payload", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Result channel not delivered within 100ms")
	}
}

func TestAwaits_ResolveTool_NotFound(t *testing.T) {
	a := session.NewAwaits()
	err := a.ResolveTool(&types.ToolResultPayload{ToolUseID: "unknown"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("want ErrAwaitNotFound, got %v", err)
	}
}

func TestAwaits_AbortAll_ClosesChannels(t *testing.T) {
	a := session.NewAwaits()
	aw1 := a.PushTool("u1", "Read")
	aw2 := a.PushTool("u2", "Bash")

	a.AbortAll("aborted")

	for i, ch := range []<-chan *types.ToolResultPayload{aw1.Result, aw2.Result} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("aw%d: Result delivered a value, want closed channel", i+1)
			}
		case <-time.After(10 * time.Millisecond):
			t.Errorf("aw%d: Result not closed within 10ms", i+1)
		}
	}
}

func TestAwaits_PushResolvePerm(t *testing.T) {
	a := session.NewAwaits()
	aw := a.PushPerm("perm_1")
	if aw == nil {
		t.Fatal("PushPerm returned nil")
	}

	resp := &types.PermissionResponse{RequestID: "perm_1", Approved: true}
	if err := a.ResolvePerm("perm_1", resp); err != nil {
		t.Fatalf("ResolvePerm: %v", err)
	}

	select {
	case got := <-aw.Response:
		if got == nil || !got.Approved {
			t.Errorf("got %#v, want approved response", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Response channel not delivered within 100ms")
	}
}

func TestAwaits_ResolvePerm_NotFound(t *testing.T) {
	a := session.NewAwaits()
	err := a.ResolvePerm("unknown", &types.PermissionResponse{RequestID: "unknown"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("want ErrAwaitNotFound, got %v", err)
	}
}

func TestAwaits_PushResolvePlan(t *testing.T) {
	a := session.NewAwaits()
	aw := a.PushPlan("plan_1", "session_1")
	if aw == nil {
		t.Fatal("PushPlan returned nil")
	}

	resp := &types.PlanResponse{PlanID: "plan_1", Approved: true}
	if err := a.ResolvePlan("plan_1", resp); err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}

	select {
	case got := <-aw.Response:
		if got == nil || !got.Approved {
			t.Errorf("got %#v, want approved response", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Response channel not delivered within 100ms")
	}
}

func TestAwaits_ResolvePlan_NotFound(t *testing.T) {
	a := session.NewAwaits()
	err := a.ResolvePlan("unknown", &types.PlanResponse{PlanID: "unknown"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("want ErrAwaitNotFound, got %v", err)
	}
}

func TestAwaits_PushResolveStepDecision(t *testing.T) {
	a := session.NewAwaits()
	aw := a.PushStepDecision("step_1", "session_1")
	if aw == nil {
		t.Fatal("PushStepDecision returned nil")
	}

	resp := &types.StepDecisionResponse{RequestID: "step_1", Decision: "continue"}
	if err := a.ResolveStepDecision("step_1", resp); err != nil {
		t.Fatalf("ResolveStepDecision: %v", err)
	}

	select {
	case got := <-aw.Response:
		if got == nil || got.Decision != "continue" {
			t.Errorf("got %#v, want continue response", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Response channel not delivered within 100ms")
	}
}

func TestAwaits_ResolveStepDecision_NotFound(t *testing.T) {
	a := session.NewAwaits()
	err := a.ResolveStepDecision("unknown", &types.StepDecisionResponse{RequestID: "unknown"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("want ErrAwaitNotFound, got %v", err)
	}
}

func TestAwaits_ForgetTool(t *testing.T) {
	a := session.NewAwaits()
	aw := a.PushTool("u1", "Read")

	a.ForgetTool("u1")

	// Subsequent ResolveTool should report NotFound.
	err := a.ResolveTool(&types.ToolResultPayload{ToolUseID: "u1"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("after ForgetTool, ResolveTool returned %v; want ErrAwaitNotFound", err)
	}

	// The channel from the now-discarded await stays open (no close).
	// The caller is expected to abandon it. Reading from it should block.
	select {
	case <-aw.Result:
		t.Error("Result channel produced a value after ForgetTool")
	default:
		// Expected: still open and empty.
	}
}

func TestAwaits_ForgetTool_Missing(t *testing.T) {
	a := session.NewAwaits()
	// Should not panic, not error.
	a.ForgetTool("unknown")
}

func TestAwaits_ForgetPerm(t *testing.T) {
	a := session.NewAwaits()
	a.PushPerm("req_1")
	a.ForgetPerm("req_1")
	err := a.ResolvePerm("req_1", &types.PermissionResponse{RequestID: "req_1"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("after ForgetPerm, ResolvePerm returned %v; want ErrAwaitNotFound", err)
	}
}

func TestAwaits_ForgetPerm_Missing(t *testing.T) {
	a := session.NewAwaits()
	a.ForgetPerm("unknown") // must not panic
}

func TestAwaits_ForgetPlan(t *testing.T) {
	a := session.NewAwaits()
	a.PushPlan("plan_1", "sess_1")
	a.ForgetPlan("plan_1")
	err := a.ResolvePlan("plan_1", &types.PlanResponse{PlanID: "plan_1"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("after ForgetPlan, ResolvePlan returned %v; want ErrAwaitNotFound", err)
	}
}

func TestAwaits_ForgetPlan_Missing(t *testing.T) {
	a := session.NewAwaits()
	a.ForgetPlan("unknown") // must not panic
}

func TestAwaits_ForgetStepDecision(t *testing.T) {
	a := session.NewAwaits()
	a.PushStepDecision("req_1", "sess_1")
	a.ForgetStepDecision("req_1")
	err := a.ResolveStepDecision("req_1", &types.StepDecisionResponse{RequestID: "req_1"})
	if !errors.Is(err, session.ErrAwaitNotFound) {
		t.Errorf("after ForgetStepDecision, Resolve returned %v; want ErrAwaitNotFound", err)
	}
}

func TestAwaits_ForgetStepDecision_Missing(t *testing.T) {
	a := session.NewAwaits()
	a.ForgetStepDecision("unknown") // must not panic
}

func TestAwaits_AbortAll_All4Kinds(t *testing.T) {
	a := session.NewAwaits()
	toolAw := a.PushTool("u1", "Read")
	permAw := a.PushPerm("perm_1")
	planAw := a.PushPlan("plan_1", "session_1")
	stepAw := a.PushStepDecision("step_1", "session_1")

	a.AbortAll("aborted")

	// Check tool result channel closed
	select {
	case _, ok := <-toolAw.Result:
		if ok {
			t.Error("tool: Result delivered a value, want closed channel")
		}
	case <-time.After(10 * time.Millisecond):
		t.Error("tool: Result not closed within 10ms")
	}

	// Check perm response channel closed
	select {
	case _, ok := <-permAw.Response:
		if ok {
			t.Error("perm: Response delivered a value, want closed channel")
		}
	case <-time.After(10 * time.Millisecond):
		t.Error("perm: Response not closed within 10ms")
	}

	// Check plan response channel closed
	select {
	case _, ok := <-planAw.Response:
		if ok {
			t.Error("plan: Response delivered a value, want closed channel")
		}
	case <-time.After(10 * time.Millisecond):
		t.Error("plan: Response not closed within 10ms")
	}

	// Check step decision response channel closed
	select {
	case _, ok := <-stepAw.Response:
		if ok {
			t.Error("step: Response delivered a value, want closed channel")
		}
	case <-time.After(10 * time.Millisecond):
		t.Error("step: Response not closed within 10ms")
	}
}
