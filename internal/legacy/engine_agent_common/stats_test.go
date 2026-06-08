package common_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/legacy/sessionstats"
)

func TestWithSubAgentStats_AllKeysPresent(t *testing.T) {
	ctx := common.WithSubAgentStats(context.Background(),
		"own_sid", "run_id", "parent_sid", "root_sid")

	if got, ok := sessionstats.SessionIDFromCtx(ctx); !ok || got != "own_sid" {
		t.Errorf("SessionID = (%q, %v), want (own_sid, true)", got, ok)
	}
	if got, ok := sessionstats.AgentRunIDFromCtx(ctx); !ok || got != "run_id" {
		t.Errorf("AgentRunID = (%q, %v), want (run_id, true)", got, ok)
	}
	if got, ok := sessionstats.ImmediateParentSessionIDFromCtx(ctx); !ok || got != "parent_sid" {
		t.Errorf("ImmediateParentSessionID = (%q, %v), want (parent_sid, true)", got, ok)
	}
	if got, ok := sessionstats.RootSessionIDFromCtx(ctx); !ok || got != "root_sid" {
		t.Errorf("RootSessionID = (%q, %v), want (root_sid, true)", got, ok)
	}
}

func TestWithSubAgentStats_EmptyRootFallsBackToImmediate(t *testing.T) {
	ctx := common.WithSubAgentStats(context.Background(),
		"own", "run", "parent", "")

	if got, ok := sessionstats.RootSessionIDFromCtx(ctx); !ok || got != "parent" {
		t.Errorf("RootSessionID fallback = (%q, %v), want (parent, true)", got, ok)
	}
}
