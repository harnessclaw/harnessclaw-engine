package tool

import (
	"context"
	"testing"
)

func TestAgentScope_PutGet(t *testing.T) {
	s := AgentScope{
		ReadScope:   []string{"/a/b", "/c/d"},
		WriteScope:  []string{"/a/b"},
		SessionRoot: "/a",
	}
	ctx := WithAgentScope(context.Background(), s)
	got, ok := AgentScopeFromCtx(ctx)
	if !ok {
		t.Fatalf("not set")
	}
	if len(got.ReadScope) != 2 || got.WriteScope[0] != "/a/b" || got.SessionRoot != "/a" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestAgentScope_AbsentReturnsZero(t *testing.T) {
	got, ok := AgentScopeFromCtx(context.Background())
	if ok || got.SessionRoot != "" {
		t.Errorf("expected zero+false, got %+v ok=%v", got, ok)
	}
}
