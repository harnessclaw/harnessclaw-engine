package sessionstats

import (
	"context"
	"testing"
)

func TestSessionIDFromCtx(t *testing.T) {
	if got, ok := SessionIDFromCtx(context.Background()); ok || got != "" {
		t.Errorf("empty ctx: got %q ok=%v, want \"\" false", got, ok)
	}
	ctx := WithSessionID(context.Background(), "sess_abc")
	got, ok := SessionIDFromCtx(ctx)
	if !ok || got != "sess_abc" {
		t.Errorf("got %q ok=%v, want sess_abc true", got, ok)
	}
}

func TestAgentRunIDFromCtx(t *testing.T) {
	ctx := WithAgentRunID(context.Background(), "run_x")
	got, ok := AgentRunIDFromCtx(ctx)
	if !ok || got != "run_x" {
		t.Errorf("got %q ok=%v, want run_x true", got, ok)
	}
}

func TestWithAgentRunID_Overrides(t *testing.T) {
	ctx := WithAgentRunID(context.Background(), "run_a")
	ctx = WithAgentRunID(ctx, "run_b")
	got, _ := AgentRunIDFromCtx(ctx)
	if got != "run_b" {
		t.Errorf("override: got %q, want run_b", got)
	}
}
