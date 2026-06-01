package agent

import (
	"context"
	"testing"
)

func TestParentAgentID_RoundTrip(t *testing.T) {
	ctx := WithParentAgentID(context.Background(), "sess_abc")
	got := ParentAgentIDFromCtx(ctx)
	if got != "sess_abc" {
		t.Errorf("got %q, want sess_abc", got)
	}
}

func TestParentAgentID_EmptyIsNoOp(t *testing.T) {
	// Passing "" must not overwrite an earlier non-empty id and must
	// not attach a zero-value key (defensive: the WithSubAgentStats
	// callsite chains many ctx values, an accidental empty here
	// shouldn't silently mask a real id from an outer caller).
	ctx := WithParentAgentID(context.Background(), "outer")
	ctx = WithParentAgentID(ctx, "")
	if got := ParentAgentIDFromCtx(ctx); got != "outer" {
		t.Errorf("empty WithParentAgentID overwrote outer; got %q", got)
	}
}

func TestParentAgentID_MissingReturnsEmpty(t *testing.T) {
	if got := ParentAgentIDFromCtx(context.Background()); got != "" {
		t.Errorf("got %q, want empty for ctx without key", got)
	}
	if got := ParentAgentIDFromCtx(nil); got != "" {
		t.Errorf("nil ctx must return empty, got %q", got)
	}
}
