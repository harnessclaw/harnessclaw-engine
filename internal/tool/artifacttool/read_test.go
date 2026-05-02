package artifacttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
)

func seed(t *testing.T, store artifact.Store) string {
	t.Helper()
	a, err := store.Save(context.Background(), &artifact.SaveInput{
		Type:        artifact.TypeFile,
		Name:        "doc.md",
		Description: "fixture",
		Content:     strings.Repeat("ABCDEFGH", 80), // 640 bytes
		Producer:    artifact.Producer{AgentID: "agent_a"},
		TraceID:     "tr_1",
		SessionID:   "sess_1",
	})
	if err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	return a.ID
}

func TestRead_DefaultModeIsPreviewWithoutFullContent(t *testing.T) {
	// Doc §5: scan-then-fetch. Default 'preview' must NOT leak full
	// content into the LLM, only the preview field.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	id := seed(t, store)
	rt := NewReadTool()
	ctx := newTestCtx(store, tool.ArtifactProducer{AgentID: "agent_a", SessionID: "sess_1", TraceID: "tr_1"})

	in := json.RawMessage(`{"artifact_id":"` + id + `"}`)
	if err := rt.ValidateInput(in); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	res, err := rt.Execute(ctx, in)
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isErr=%v content=%s", err, res.IsError, res.Content)
	}

	var resp struct {
		Mode     string             `json:"mode"`
		Artifact *artifact.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal([]byte(res.Content), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Mode != "preview" {
		t.Errorf("default mode = %q, want preview", resp.Mode)
	}
	if resp.Artifact.Content != "" {
		t.Error("preview mode must strip full Content")
	}
	if resp.Artifact.Preview == "" {
		t.Error("preview mode must include Preview field")
	}
}

func TestRead_FullModeIncludesContent(t *testing.T) {
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	id := seed(t, store)
	rt := NewReadTool()
	ctx := newTestCtx(store, tool.ArtifactProducer{AgentID: "agent_a"})

	in := json.RawMessage(`{"artifact_id":"` + id + `","mode":"full"}`)
	res, err := rt.Execute(ctx, in)
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isErr=%v content=%s", err, res.IsError, res.Content)
	}
	var resp struct {
		Artifact *artifact.Artifact `json:"artifact"`
	}
	_ = json.Unmarshal([]byte(res.Content), &resp)
	if resp.Artifact.Content == "" {
		t.Error("full mode must populate Content")
	}
}

func TestRead_RejectsMalformedID(t *testing.T) {
	rt := NewReadTool()
	in := json.RawMessage(`{"artifact_id":"not-a-real-id"}`)
	if err := rt.ValidateInput(in); err == nil {
		t.Errorf("ValidateInput accepted malformed id (would let LLM hallucinations through)")
	}
}

func TestRead_MissingIDReturnsRecoverableError(t *testing.T) {
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	rt := NewReadTool()
	ctx := newTestCtx(store, tool.ArtifactProducer{AgentID: "agent_a"})

	in := json.RawMessage(`{"artifact_id":"art_000000000000000000000000"}`)
	res, err := rt.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError")
	}
	if !strings.Contains(res.Content, "not found") {
		t.Errorf("error message should mention 'not found'; got %q", res.Content)
	}
}

func TestRead_BadModeRejected(t *testing.T) {
	rt := NewReadTool()
	in := json.RawMessage(`{"artifact_id":"art_000000000000000000000000","mode":"all"}`)
	if err := rt.ValidateInput(in); err == nil {
		t.Errorf("ValidateInput accepted invalid mode")
	}
}
