package multimodal

import (
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestBuild_TextOnlyPassesThrough(t *testing.T) {
	blocks, err := Build(
		"hello",
		[]types.IncomingContentBlock{{Type: "text", Text: "hello"}},
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeText || blocks[0].Text != "hello" {
		t.Fatalf("unexpected: %+v", blocks)
	}
}

func TestBuild_EmptyContentFallsBackToText(t *testing.T) {
	// Legacy v1 path: caller didn't send Content[], only top-level Text.
	// Builder must preserve back-compat.
	blocks, err := Build("legacy text", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Text != "legacy text" {
		t.Fatalf("expected single text block, got %+v", blocks)
	}
}

func TestBuild_Base64Image(t *testing.T) {
	blocks, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/png", Data: "iVBORw0KGgo="},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeImage {
		t.Fatalf("expected image block, got %+v", blocks)
	}
	if blocks[0].MediaType != "image/png" || blocks[0].Data == "" {
		t.Fatalf("image fields lost: %+v", blocks[0])
	}
}

func TestBuild_UrlImage(t *testing.T) {
	blocks, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/jpeg", URL: "https://example.com/x.jpg"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if blocks[0].URL == "" {
		t.Errorf("url lost: %+v", blocks[0])
	}
}

func TestBuild_RejectsImageMissingMediaType(t *testing.T) {
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "image", Data: "iVBORw0KGgo="},
	})
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("wrong type: %T", err)
	}
}

func TestBuild_RejectsImageNoSource(t *testing.T) {
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/png"},
	})
	if err == nil {
		t.Fatal("expected validation error for missing data and url")
	}
}

func TestBuild_RejectsOversizedBase64(t *testing.T) {
	big := strings.Repeat("A", MaxBase64BlockBytes+1)
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/png", Data: big},
	})
	if err == nil {
		t.Fatal("expected validation error for oversized data")
	}
}

func TestBuild_RejectsCumulativeOversize(t *testing.T) {
	// Each block individually under MaxBase64BlockBytes, but the sum
	// crosses MaxTotalBytesPerMessage. Defense against many-small-images
	// flooding context.
	half := strings.Repeat("A", MaxBase64BlockBytes)
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/png", Data: half},
		{Type: "image", MIMEType: "image/png", Data: half},
		{Type: "image", MIMEType: "image/png", Data: half}, // 3×10MB = 30MB total > 20MB cap
	})
	if err == nil {
		t.Fatal("expected validation error for cumulative oversize")
	}
}

func TestBuild_RejectsUnknownType(t *testing.T) {
	// Video isn't supported in this phase. Builder must reject so a
	// future provider rollout doesn't silently pass through unvetted
	// modalities.
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "video", URL: "https://example.com/v.mp4"},
	})
	if err == nil {
		t.Fatal("expected validation error for video (not yet supported)")
	}
}

func TestBuild_MixesTextAndImage(t *testing.T) {
	blocks, err := Build("", []types.IncomingContentBlock{
		{Type: "text", Text: "这张图说啥？"},
		{Type: "image", MIMEType: "image/png", Data: "iVBORw0KGgo="},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[1].Type != types.ContentTypeImage {
		t.Fatalf("order or types wrong: %+v", blocks)
	}
}

func TestBuild_PdfBecomesFileBlock(t *testing.T) {
	blocks, err := Build("", []types.IncomingContentBlock{
		{Type: "pdf", MIMEType: "application/pdf", Data: "JVBERi0x"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if blocks[0].Type != types.ContentTypeFile {
		t.Fatalf("pdf must become ContentTypeFile, got %v", blocks[0].Type)
	}
}

// TestBuild_RejectsSVG locks in the security-relevant carve-out: SVG
// can carry inline <script> and most LLM vision models don't render
// it usefully. The MIME whitelist must continue to reject it even if
// future contributors add other image formats.
func TestBuild_RejectsSVG(t *testing.T) {
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "image", MIMEType: "image/svg+xml", Data: "PHN2Zy8+"},
	})
	if err == nil {
		t.Fatal("expected ValidationError for SVG")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("wrong type: %T", err)
	}
}

// TestBuild_RejectsUnknownImageMIME verifies the whitelist is closed
// — uncommon image formats (BMP / TIFF / ICO) are rejected rather
// than passed through to the upstream provider.
func TestBuild_RejectsUnknownImageMIME(t *testing.T) {
	for _, mt := range []string{"image/bmp", "image/tiff", "image/x-icon"} {
		_, err := Build("", []types.IncomingContentBlock{
			{Type: "image", MIMEType: mt, Data: "AA=="},
		})
		if err == nil {
			t.Errorf("%s should be rejected", mt)
		}
	}
}

func TestBuild_AcceptsAllWhitelistedImageMIMEs(t *testing.T) {
	for _, mt := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		_, err := Build("", []types.IncomingContentBlock{
			{Type: "image", MIMEType: mt, Data: "AA=="},
		})
		if err != nil {
			t.Errorf("%s should be accepted: %v", mt, err)
		}
	}
}

func TestBuild_RejectsNonPdfDocumentMIME(t *testing.T) {
	// .docx etc. flow through the legacy text-attachment path, not
	// as native multimodal blocks. The Build whitelist must not
	// quietly accept them.
	_, err := Build("", []types.IncomingContentBlock{
		{Type: "pdf", MIMEType: "application/msword", Data: "AA=="},
	})
	if err == nil {
		t.Fatal("expected validation error for non-pdf document MIME")
	}
}

func TestBuild_DropsEmptyTextBlocks(t *testing.T) {
	// Defensive — clients sometimes send a trailing empty text block
	// when only attachments are attached. Builder shouldn't turn that
	// into an empty Text content block downstream.
	blocks, err := Build("", []types.IncomingContentBlock{
		{Type: "text", Text: ""},
		{Type: "image", MIMEType: "image/png", Data: "AA=="},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeImage {
		t.Fatalf("empty text not dropped: %+v", blocks)
	}
}
