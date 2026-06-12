package multimodal

import (
	"testing"

	"harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/pkg/types"
)

func TestGate_TextOnlyPassesAnyModel(t *testing.T) {
	blocks := []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}
	if err := Gate("openai:gpt-3.5", registry.SupportsFlags{}, blocks); err != nil {
		t.Fatalf("text must pass even with empty supports: %v", err)
	}
}

// Images are intentionally NOT gated anymore: tools (image_generate,
// video_create i2v, browser agent) can consume them, so a non-vision
// chat model is no reason to reject the message — pass through and let
// the downstream model/provider decide.
func TestGate_ImagePassesEvenWithoutVision(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeImage, MediaType: "image/png", Data: "x"},
	}
	if err := Gate("anthropic:claude-haiku-4-5", registry.SupportsFlags{}, blocks); err != nil {
		t.Fatalf("image must pass through even without Vision: %v", err)
	}
}

func TestGate_ImageAcceptedByVisionModel(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeImage, MediaType: "image/png", Data: "x"},
	}
	if err := Gate("anthropic:claude-opus-4-7", registry.SupportsFlags{Vision: true}, blocks); err != nil {
		t.Fatalf("vision model must accept image: %v", err)
	}
}

func TestGate_DedupRejected(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeFile, MediaType: "application/pdf"},
		{Type: types.ContentTypeFile, MediaType: "application/pdf"},
	}
	err := Gate("x:y", registry.SupportsFlags{}, blocks)
	u, _ := err.(*UnsupportedModalityError)
	if u == nil || len(u.RejectedModalities) != 1 {
		t.Fatalf("duplicates not deduped: %+v", u)
	}
}

func TestGate_PdfNeedsPDFInputNotVision(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeFile, MediaType: "application/pdf"},
	}
	if err := Gate("x:y", registry.SupportsFlags{Vision: true}, blocks); err == nil {
		t.Fatal("Vision alone shouldn't accept PDF")
	}
	if err := Gate("x:y", registry.SupportsFlags{PDFInput: true}, blocks); err != nil {
		t.Fatalf("PDFInput=true must accept pdf: %v", err)
	}
}

func TestGate_NonPdfFileSkipsGate(t *testing.T) {
	// Non-PDF file blocks (e.g. .csv attachments routed via the legacy
	// text-JSON path; not actually constructed by Build today) have no
	// well-known modality token — they should pass through silently
	// rather than fail closed.
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeFile, MediaType: "text/csv"},
	}
	if err := Gate("x:y", registry.SupportsFlags{}, blocks); err != nil {
		t.Errorf("non-pdf file must pass: %v", err)
	}
}

func TestGate_MixedImageAndPdfOnlyPdfRejected(t *testing.T) {
	// Images pass through (tool-consumable); pdf is still gated. A mixed
	// message must reject ONLY the pdf modality.
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeFile, MediaType: "application/pdf"},
		{Type: types.ContentTypeImage, MediaType: "image/png"},
	}
	err := Gate("x:y", registry.SupportsFlags{}, blocks)
	u, _ := err.(*UnsupportedModalityError)
	if u == nil {
		t.Fatal("expected error for pdf")
	}
	if len(u.RejectedModalities) != 1 || u.RejectedModalities[0] != "pdf" {
		t.Errorf("want only pdf rejected, got %v", u.RejectedModalities)
	}
}
