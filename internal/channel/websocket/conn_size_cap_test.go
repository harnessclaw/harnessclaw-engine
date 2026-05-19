package websocket

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/multimodal"
)

func TestCheckInlineSizeCaps_AllowsSmallPayload(t *testing.T) {
	got := checkInlineSizeCaps([]userContentBlock{
		{Type: "text", Text: "hi"},
		{Type: "image", Source: &contentSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}},
	})
	if got != "" {
		t.Errorf("expected pass, got rejection: %q", got)
	}
}

func TestCheckInlineSizeCaps_RejectsPerBlockOversize(t *testing.T) {
	big := strings.Repeat("A", multimodal.MaxBase64BlockBytes+1)
	got := checkInlineSizeCaps([]userContentBlock{
		{Type: "image", Source: &contentSource{Type: "base64", MediaType: "image/png", Data: big}},
	})
	if got == "" {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(got, "base64") {
		t.Errorf("rejection should mention base64 cap: %q", got)
	}
}

func TestCheckInlineSizeCaps_RejectsCumulative(t *testing.T) {
	chunk := strings.Repeat("A", multimodal.MaxBase64BlockBytes)
	got := checkInlineSizeCaps([]userContentBlock{
		{Type: "image", Source: &contentSource{Type: "base64", MediaType: "image/png", Data: chunk}},
		{Type: "image", Source: &contentSource{Type: "base64", MediaType: "image/png", Data: chunk}},
		{Type: "image", Source: &contentSource{Type: "base64", MediaType: "image/png", Data: chunk}}, // 3 × 10MB > 20MB total
	})
	if got == "" {
		t.Fatal("expected cumulative rejection")
	}
	if !strings.Contains(got, "total") {
		t.Errorf("rejection should mention total cap: %q", got)
	}
}

func TestCheckInlineSizeCaps_NilSourceSkipped(t *testing.T) {
	// Text-only blocks have Source==nil; must not trip the check.
	got := checkInlineSizeCaps([]userContentBlock{
		{Type: "text", Text: "just text"},
	})
	if got != "" {
		t.Errorf("text-only block should not trip size check: %q", got)
	}
}
