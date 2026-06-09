package middlewares

import (
	"context"
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/scheduler"
)

func TestIdentity_AssignsIDs(t *testing.T) {
	var mw scheduler.Middleware = Identity{}
	st := &scheduler.SpawnState{Bag: map[string]any{}}
	ctx, err := mw.Before(context.Background(), scheduler.SpawnParams{}, st)
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if !strings.HasPrefix(string(st.AgentID), "a-") {
		t.Errorf("AgentID %q lacks a- prefix", st.AgentID)
	}
	if !strings.HasPrefix(string(st.TaskID), "t-") {
		t.Errorf("TaskID %q lacks t- prefix", st.TaskID)
	}
}

func TestIdentity_UniquePerCall(t *testing.T) {
	var mw scheduler.Middleware = Identity{}
	a := &scheduler.SpawnState{Bag: map[string]any{}}
	b := &scheduler.SpawnState{Bag: map[string]any{}}
	_, _ = mw.Before(context.Background(), scheduler.SpawnParams{}, a)
	_, _ = mw.Before(context.Background(), scheduler.SpawnParams{}, b)
	if a.AgentID == b.AgentID {
		t.Error("AgentIDs collided")
	}
	if a.TaskID == b.TaskID {
		t.Error("TaskIDs collided")
	}
}
