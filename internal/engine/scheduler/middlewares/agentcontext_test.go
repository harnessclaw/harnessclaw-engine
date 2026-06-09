package middlewares

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/scheduler"
	pkgtypes "harnessclaw-go/pkg/types"
)

func TestAgentContext_InjectsValue(t *testing.T) {
	var mw scheduler.Middleware = AgentContext{}
	st := &scheduler.SpawnState{
		AgentID: "a-x", TaskID: "t-x",
		Bag: map[string]any{},
	}
	p := scheduler.SpawnParams{
		Parent: &scheduler.ParentRef{
			AgentID: "a-parent", SessionID: pkgtypes.SessionID("s-1"),
		},
		InvokedBy: scheduler.Invoker{Kind: scheduler.InvokerLLM},
	}
	ctx, err := mw.Before(context.Background(), p, st)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := AgentCtxFrom(ctx)
	if !ok {
		t.Fatal("AgentCtxFrom returned not ok")
	}
	if v.AgentID != "a-x" {
		t.Errorf("AgentID got %q", v.AgentID)
	}
	if v.ParentAgentID != "a-parent" {
		t.Errorf("ParentAgentID got %q", v.ParentAgentID)
	}
	if v.SessionID != "s-1" {
		t.Errorf("SessionID got %q", v.SessionID)
	}
}

func TestAgentContext_NoParent(t *testing.T) {
	mw := AgentContext{}
	st := &scheduler.SpawnState{AgentID: "a-x", TaskID: "t-x", Bag: map[string]any{}}
	ctx, _ := mw.Before(context.Background(), scheduler.SpawnParams{}, st)
	v, _ := AgentCtxFrom(ctx)
	if v.ParentAgentID != "" {
		t.Errorf("ParentAgentID should be empty, got %q", v.ParentAgentID)
	}
	if v.SessionID != "" {
		t.Errorf("SessionID should be empty, got %q", v.SessionID)
	}
}
