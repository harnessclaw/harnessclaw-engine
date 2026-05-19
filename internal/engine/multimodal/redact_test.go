package multimodal

import (
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestRedactBlocksForLog_ReplacesBase64(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "hi"},
		{Type: types.ContentTypeImage, MediaType: "image/png", Data: "iVBORw0KGgo="},
		{Type: types.ContentTypeFile, MediaType: "application/pdf", Data: "JVBERi0x", Filename: "doc.pdf"},
	}
	out := RedactBlocksForLog(blocks)

	if out[0].Text != "hi" {
		t.Error("text mutated")
	}
	if out[1].Data == "iVBORw0KGgo=" || !strings.Contains(out[1].Data, "redacted") {
		t.Errorf("image data not redacted: %q", out[1].Data)
	}
	if out[2].Data == "JVBERi0x" || !strings.Contains(out[2].Data, "redacted") {
		t.Errorf("pdf data not redacted: %q", out[2].Data)
	}
	// Non-data fields survive — these are needed for routing /
	// capability debugging.
	if out[1].MediaType != "image/png" {
		t.Errorf("image media_type lost: %q", out[1].MediaType)
	}
	if out[2].Filename != "doc.pdf" {
		t.Errorf("pdf filename lost: %q", out[2].Filename)
	}

	// Source slice must not be mutated — otherwise the caller's
	// in-memory message gets corrupted by a logging-side effect.
	if blocks[1].Data != "iVBORw0KGgo=" {
		t.Error("redactor mutated source blocks")
	}
}

func TestRedactBlocksForLog_EmptyDataLeftAlone(t *testing.T) {
	// URL-only blocks (no inline base64) should pass through
	// unchanged. The redact summary is only meaningful when there's
	// payload to truncate.
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeImage, MediaType: "image/jpeg", URL: "https://x/y.jpg"},
	}
	out := RedactBlocksForLog(blocks)
	if out[0].URL != "https://x/y.jpg" {
		t.Errorf("url stripped: %+v", out[0])
	}
	if out[0].Data != "" {
		t.Errorf("empty data should stay empty, got %q", out[0].Data)
	}
}
