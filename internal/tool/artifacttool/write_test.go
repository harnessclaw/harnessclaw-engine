package artifacttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
)

func newTestCtx(store artifact.Store, p tool.ArtifactProducer) context.Context {
	ctx := context.Background()
	ctx = tool.WithArtifactStoreValue(ctx, store)
	ctx = tool.WithArtifactProducer(ctx, p)
	return ctx
}

func TestWrite_PersistsAndReturnsID(t *testing.T) {
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	wt := NewWriteTool()

	ctx := newTestCtx(store, tool.ArtifactProducer{
		AgentID:   "agent_test",
		SessionID: "sess_1",
		TraceID:   "tr_1",
	})

	in := json.RawMessage(`{
		"type":"file",
		"name":"hello.md",
		"description":"smoke",
		"mime_type":"text/markdown",
		"content":"hello world"
	}`)
	if err := wt.ValidateInput(in); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	res, err := wt.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	var resp struct {
		ArtifactID string `json:"artifact_id"`
		Size       int    `json:"size_bytes"`
		Version    int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(res.Content), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !artifact.IsValidID(resp.ArtifactID) {
		t.Errorf("response artifact_id %q is malformed", resp.ArtifactID)
	}
	if resp.Size != 11 {
		t.Errorf("size = %d, want 11", resp.Size)
	}
	if resp.Version != 1 {
		t.Errorf("version = %d, want 1", resp.Version)
	}

	// Round-trip via store: producer stamp must come from context, not LLM.
	a, err := store.Get(ctx, resp.ArtifactID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if a.Producer.AgentID != "agent_test" {
		t.Errorf("producer.agent_id = %q, want agent_test (was the engine stamp ignored?)", a.Producer.AgentID)
	}
	if a.SessionID != "sess_1" || a.TraceID != "tr_1" {
		t.Errorf("scope ids missing: session=%q trace=%q", a.SessionID, a.TraceID)
	}
}

func TestWrite_MissingStoreErrorsCleanly(t *testing.T) {
	// Tools must never crash when the engine forgot to inject the store —
	// the LLM should see a clear error and re-plan.
	wt := NewWriteTool()
	in := json.RawMessage(`{"type":"file","content":"x"}`)
	res, err := wt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true; got content=%q", res.Content)
	}
	if !strings.Contains(res.Content, "not configured") {
		t.Errorf("error message should explain the misconfiguration; got %q", res.Content)
	}
}

func TestWrite_ValidatesType(t *testing.T) {
	wt := NewWriteTool()
	bad := json.RawMessage(`{"type":"image","content":"x"}`)
	if err := wt.ValidateInput(bad); err == nil {
		t.Errorf("ValidateInput accepted invalid type")
	}
}

func TestWrite_RejectsEmptyContent(t *testing.T) {
	wt := NewWriteTool()
	bad := json.RawMessage(`{"type":"file"}`)
	if err := wt.ValidateInput(bad); err == nil {
		t.Errorf("ValidateInput accepted missing content")
	}
}
