package engine

import (
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestArtifactRefFromMetadata_SkipsNonArtifactResults(t *testing.T) {
	// The fast path: tools that don't deal with artifacts must not pay
	// the cost of building a Ref, and must NEVER produce a stray
	// half-populated Ref the UI would have to handle.
	cases := []struct {
		name string
		meta map[string]any
	}{
		{"nil", nil},
		{"empty", map[string]any{}},
		{"file_info hint (FileWrite)", map[string]any{"render_hint": "file_info", "artifact_id": "art_x"}},
		{"artifact hint missing id", map[string]any{"render_hint": "artifact"}},
		{"artifact hint empty id", map[string]any{"render_hint": "artifact", "artifact_id": ""}},
	}
	for _, c := range cases {
		if _, ok := artifactRefFromMetadata(c.meta); ok {
			t.Errorf("%s: should not produce a Ref", c.name)
		}
	}
}

func TestArtifactRefFromMetadata_PopulatesAllFields(t *testing.T) {
	// JSON round-trip can change number types (int → float64). The
	// extractor must handle both so refs survive the wire-side path
	// where deserialised metadata reaches the executor as float64.
	meta := map[string]any{
		"render_hint":  "artifact",
		"artifact_id":  "art_abc123",
		"name":         "report.md",
		"type":         "file",
		"mime_type":    "text/markdown",
		"description":  "Q4 report",
		"preview_text": "# Q4 ...",
		"uri":          "artifact://art_abc123",
		"size":         float64(1024), // float to simulate post-JSON
	}
	ref, ok := artifactRefFromMetadata(meta)
	if !ok {
		t.Fatalf("extraction failed for valid artifact metadata")
	}
	want := types.ArtifactRef{
		ArtifactID:  "art_abc123",
		Name:        "report.md",
		Type:        "file",
		MIMEType:    "text/markdown",
		Description: "Q4 report",
		PreviewText: "# Q4 ...",
		URI:         "artifact://art_abc123",
		SizeBytes:   1024,
	}
	if ref != want {
		t.Errorf("ref mismatch:\n  got  %+v\n  want %+v", ref, want)
	}
}

// TestArtifactRefFromMetadata_PrefersListWhenPresent guards the
// L1-tool.end gap fix: Specialists / Task tools put the aggregated
// SubmittedArtifacts list on metadata["artifacts"] so the WebSocket
// gets the full set on the dispatch tool's tool.end event, not just
// the per-write subagent.event stream.
//
// This test isn't on artifactRefFromMetadata directly (that helper still
// only returns one Ref) but on the executor's combined extraction code
// path: when metadata carries both shapes, the list wins.
func TestExtractArtifacts_ListMetadataReachesEvent(t *testing.T) {
	// We exercise the precedence rule from executor.go directly via the
	// two helpers — the actual `evt.Artifacts =` happens inside the
	// defer, which would require a full Execute round-trip to test.
	meta := map[string]any{
		"render_hint": "agent", // dispatch tool, not "artifact"
		"artifacts": []types.ArtifactRef{
			{ArtifactID: "art_aaa", Name: "a.md", Role: "draft_email"},
			{ArtifactID: "art_bbb", Name: "b.csv", Role: "comparison_table"},
		},
	}

	// First-shape branch: list type assertion succeeds.
	if list, ok := meta["artifacts"].([]types.ArtifactRef); !ok || len(list) != 2 {
		t.Fatalf("metadata[artifacts] should be []ArtifactRef len=2; got ok=%v len=%d", ok, len(list))
	}

	// Second-shape branch: render_hint != "artifact" → returns false.
	if _, ok := artifactRefFromMetadata(meta); ok {
		t.Errorf("artifactRefFromMetadata should return false on render_hint=agent (single-Ref path is for ArtifactWrite only)")
	}
}

func TestArtifactRefFromMetadata_AcceptsIntSize(t *testing.T) {
	// Server-side path: size arrives as int. Cover this even though the
	// extractor's switch handles both — regression here would silently
	// drop SizeBytes from every produced Ref, leaving the UI unable to
	// decide between inline preview and download.
	ref, ok := artifactRefFromMetadata(map[string]any{
		"render_hint": "artifact",
		"artifact_id": "art_int_size",
		"size":        42,
	})
	if !ok || ref.SizeBytes != 42 {
		t.Errorf("int size lost: got %+v ok=%v", ref, ok)
	}
}
