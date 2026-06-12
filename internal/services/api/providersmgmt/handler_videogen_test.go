package providersmgmt

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPatchAgentRequestDecodesVideoGeneration(t *testing.T) {
	t.Parallel()
	var req PatchAgentRequest
	if err := json.Unmarshal([]byte(`{"video_generation":"doubao:seedance-lite-i2v"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.VideoGeneration == nil || *req.VideoGeneration != "doubao:seedance-lite-i2v" {
		t.Fatalf("video_generation not decoded: %+v", req.VideoGeneration)
	}
}

func TestPatch_Agent_VideoGeneration_RoundTrip(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/agent",
		`{"video_generation":"doubao:seedance-lite-i2v"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.mgr.CurrentAgent().VideoGeneration; got != "doubao:seedance-lite-i2v" {
		t.Fatalf("CurrentAgent().VideoGeneration = %q, want %q", got, "doubao:seedance-lite-i2v")
	}
}
