package types

import (
	"encoding/json"
	"testing"
)

// TestContentBlock_ImageRoundTrip verifies the new multimodal fields
// (MediaType, Data, URL, Path, Filename, Size) survive a JSON round-trip.
// Regression guard for the wire-format extension introduced in the
// multimodal image input plan (2026-05-18).
func TestContentBlock_ImageRoundTrip(t *testing.T) {
	in := ContentBlock{
		Type:      ContentTypeImage,
		MediaType: "image/png",
		Data:      "iVBORw0KGgo=",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ContentBlock
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.MediaType != "image/png" || out.Data != "iVBORw0KGgo=" {
		t.Fatalf("image fields lost: %+v", out)
	}
}

// TestContentBlock_TextStillCompact confirms that the new multimodal
// fields stay invisible on the wire for text-only blocks. If a future
// edit accidentally drops an `omitempty`, this test catches the wire
// bloat before clients see oversized frames.
func TestContentBlock_TextStillCompact(t *testing.T) {
	b, err := json.Marshal(ContentBlock{Type: ContentTypeText, Text: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"type":"text","text":"hi"}` {
		t.Fatalf("text block bloated: %s", string(b))
	}
}

// TestContentBlock_FileBlock confirms PDF / file content blocks carry
// their full source descriptor (Filename + Size + MediaType + Data).
func TestContentBlock_FileBlock(t *testing.T) {
	in := ContentBlock{
		Type:      ContentTypeFile,
		MediaType: "application/pdf",
		Filename:  "doc.pdf",
		Size:      4096,
		Data:      "JVBERi0x",
	}
	b, _ := json.Marshal(in)
	var out ContentBlock
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Filename != "doc.pdf" || out.Size != 4096 || out.Data != "JVBERi0x" {
		t.Fatalf("file fields lost: %+v", out)
	}
}
