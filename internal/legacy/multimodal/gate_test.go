package multimodal

import (
	"errors"
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

func TestGate_ImageRejectedByNonVisionModel(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeImage, MediaType: "image/png", Data: "x"},
	}
	err := Gate("anthropic:claude-haiku-4-5", registry.SupportsFlags{}, blocks)
	if err == nil {
		t.Fatal("expected UnsupportedModalityError")
	}
	var u *UnsupportedModalityError
	if !errors.As(err, &u) {
		t.Fatalf("wrong error type: %T", err)
	}
	if u.Model != "anthropic:claude-haiku-4-5" {
		t.Errorf("model field: %q", u.Model)
	}
	if len(u.RejectedModalities) != 1 || u.RejectedModalities[0] != "image" {
		t.Errorf("rejected list: %v", u.RejectedModalities)
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
		{Type: types.ContentTypeImage, MediaType: "image/png"},
		{Type: types.ContentTypeImage, MediaType: "image/jpeg"},
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

func TestGate_MultipleRejectedSorted(t *testing.T) {
	// Sub-agent in future could carry multiple modality types in one
	// message. Output must be deterministic for reproducible error
	// frames + UI rendering.
	blocks := []types.ContentBlock{
		{Type: types.ContentTypeFile, MediaType: "application/pdf"},
		{Type: types.ContentTypeImage, MediaType: "image/png"},
	}
	err := Gate("x:y", registry.SupportsFlags{}, blocks)
	u, _ := err.(*UnsupportedModalityError)
	if u == nil {
		t.Fatal("expected error")
	}
	if len(u.RejectedModalities) != 2 {
		t.Fatalf("want 2 modalities, got %v", u.RejectedModalities)
	}
	if u.RejectedModalities[0] != "image" || u.RejectedModalities[1] != "pdf" {
		t.Errorf("not sorted: %v", u.RejectedModalities)
	}
}
