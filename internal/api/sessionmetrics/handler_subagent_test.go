package sessionmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/pkg/types"
)

// dummyLoader implements Loader by returning empty stats.
// Used in tests that only exercise the in-memory tracker path.
type dummyLoader struct{}

func (d *dummyLoader) LoadSessionStats(_ context.Context, _ string) (types.SessionStats, error) {
	return types.SessionStats{}, nil
}

// TestMetricsAPI_ReturnsSubAgentRows asserts that GET /api/v1/sessions/{id}/metrics
// returns a 200 JSON body whose sub_agents array contains the row opened via
// tracker.StartSubAgent + tracker.RecordLLMCall.
//
// This is the endpoint the user observed returning sub_agents:[] — after the
// executor fix (Task 1.2) ensures SpawnConfig.ParentSessionID is populated,
// the tracker will have rows and this test confirms the HTTP surface exposes them.
func TestMetricsAPI_ReturnsSubAgentRows(t *testing.T) {
	const sessionID = "sess_e2e_subagent"
	const subRunID = "run_specialists_xyz"

	// Build an in-memory tracker with a populated sub-agent row.
	reg := sessionstats.NewRegistry()
	tr := reg.GetOrCreate(sessionID)
	tr.StartSubAgent(subRunID, subRunID, "specialists", "")

	// Simulate a Chat call: RecordLLMCall with agentRunID routes tokens to
	// the sub-agent row opened above.
	tr.RecordLLMCall("sonnet-3-7", subRunID, &types.Usage{
		InputTokens:  1000,
		OutputTokens: 200,
		CacheRead:    100,
	}, 150)

	// Serve via the real Handler wired to our registry.
	h := New(reg, &dummyLoader{}, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var got types.SessionStats
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if len(got.SubAgents) != 1 {
		t.Fatalf("sub_agents len = %d, want 1; full stats: %+v", len(got.SubAgents), got)
	}

	sa := got.SubAgents[0]

	if sa.AgentRunID != subRunID {
		t.Errorf("run_id = %q, want %q", sa.AgentRunID, subRunID)
	}
	if sa.InputTokens != 1000 {
		t.Errorf("input_tokens = %d, want 1000", sa.InputTokens)
	}
	if sa.OutputTokens != 200 {
		t.Errorf("output_tokens = %d, want 200", sa.OutputTokens)
	}
	if sa.CacheReadTokens != 100 {
		t.Errorf("cache_read_tokens = %d, want 100", sa.CacheReadTokens)
	}
	if sa.AgentType != "specialists" {
		t.Errorf("agent_type = %q, want %q", sa.AgentType, "specialists")
	}
	if sa.LLMCalls != 1 {
		t.Errorf("llm_calls = %d, want 1", sa.LLMCalls)
	}
}
