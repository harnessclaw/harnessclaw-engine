package bifrost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

	"harnessclaw-go/pkg/types"
)

// TestConvertSingleMessage_Base64Image proves the bifrost adapter now
// honors ContentTypeImage blocks instead of silently dropping them.
// The block must reach the LLM SDK as a data URL so the anthropic /
// openai provider can extract media_type via the standard
// data:<mime>;base64,<...> form.
func TestConvertSingleMessage_Base64Image(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "describe this"},
			{Type: types.ContentTypeImage, MediaType: "image/png", Data: "iVBORw0KGgo="},
		},
	}
	got := convertSingleMessage(msg, false)
	if got == nil || got.Content == nil || got.Content.ContentBlocks == nil {
		t.Fatalf("expected content_blocks, got: %+v", got)
	}
	if len(got.Content.ContentBlocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(got.Content.ContentBlocks))
	}
	img := got.Content.ContentBlocks[1]
	if img.Type != schemas.ChatContentBlockTypeImage {
		t.Errorf("block type: %v", img.Type)
	}
	if img.ImageURLStruct == nil {
		t.Fatal("ImageURLStruct nil")
	}
	if !strings.HasPrefix(img.ImageURLStruct.URL, "data:image/png;base64,") {
		t.Errorf("data URL not built: %q", img.ImageURLStruct.URL)
	}
}

func TestConvertSingleMessage_UrlImage(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, MediaType: "image/jpeg", URL: "https://example.com/x.jpg"},
		},
	}
	got := convertSingleMessage(msg, false)
	if got == nil || got.Content == nil || got.Content.ContentBlocks == nil {
		t.Fatalf("expected content_blocks: %+v", got)
	}
	img := got.Content.ContentBlocks[0]
	if img.ImageURLStruct == nil {
		t.Fatal("ImageURLStruct nil")
	}
	if img.ImageURLStruct.URL != "https://example.com/x.jpg" {
		t.Errorf("url lost: %q", img.ImageURLStruct.URL)
	}
}

func TestConvertSingleMessage_PdfFile(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeFile, MediaType: "application/pdf", Data: "JVBERi0x", Filename: "doc.pdf"},
		},
	}
	got := convertSingleMessage(msg, false)
	if got == nil || got.Content == nil || got.Content.ContentBlocks == nil {
		t.Fatalf("expected content_blocks: %+v", got)
	}
	pdf := got.Content.ContentBlocks[0]
	if pdf.Type != schemas.ChatContentBlockTypeFile {
		t.Errorf("type: %v", pdf.Type)
	}
	if pdf.File == nil || pdf.File.FileData == nil {
		t.Fatal("File.FileData missing")
	}
	if *pdf.File.FileData != "JVBERi0x" {
		t.Errorf("FileData mismatch: %q", *pdf.File.FileData)
	}
	// Re-marshal to verify wire shape includes media_type so Anthropic
	// can route it correctly.
	b, _ := json.Marshal(pdf)
	if !strings.Contains(string(b), "application/pdf") {
		t.Errorf("media_type lost in marshaled form: %s", b)
	}
}

func TestConvertSingleMessage_TextOnlyStillUsesContentStr(t *testing.T) {
	// Single text block should still use the compact ContentStr form
	// (the existing optimisation must not regress when the switch grows).
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hello"},
		},
	}
	got := convertSingleMessage(msg, false)
	if got.Content.ContentStr == nil {
		t.Fatal("ContentStr should be set for single-text-block path")
	}
	if got.Content.ContentBlocks != nil {
		t.Error("ContentBlocks should be nil when ContentStr is used")
	}
}

// TestConvertSingleMessage_ImageWithoutSourceIsDropped is a
// defense-in-depth check: Build / Gate should have caught the
// missing-source case earlier, but the adapter shouldn't panic on a
// degenerate ContentBlock either.
func TestConvertSingleMessage_ImageWithoutSourceIsDropped(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "before"},
			{Type: types.ContentTypeImage, MediaType: "image/png"}, // no data, no url
		},
	}
	got := convertSingleMessage(msg, false)
	if got == nil {
		t.Fatal("nil message")
	}
	// Image was dropped; only text remains so the compact ContentStr
	// path kicks in.
	if got.Content.ContentStr == nil {
		t.Errorf("expected text-only fallback, got %+v", got.Content)
	}
}

// TestConvertSingleMessage_ImageHasEphemeralCacheControl locks in the
// prompt-cache breakpoint on every image block. Without this, every
// multi-turn request with image history re-pays ~1600 input tokens
// per image because Anthropic's prompt cache misses on the second
// turn (no cache_control = no caching).
func TestConvertSingleMessage_ImageHasEphemeralCacheControl(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, MediaType: "image/png", Data: "AA=="},
		},
	}
	got := convertSingleMessage(msg, false)
	img := got.Content.ContentBlocks[0]
	if img.CacheControl == nil {
		t.Fatal("image block missing CacheControl")
	}
	if img.CacheControl.Type != schemas.CacheControlTypeEphemeral {
		t.Errorf("wrong type: %q", img.CacheControl.Type)
	}
}

// TestConvertSingleMessage_PdfHasEphemeralCacheControl — same
// rationale as images; PDFs are typically larger so the savings are
// even higher proportionally.
func TestConvertSingleMessage_PdfHasEphemeralCacheControl(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeFile, MediaType: "application/pdf", Data: "AA==", Filename: "x.pdf"},
		},
	}
	got := convertSingleMessage(msg, false)
	pdf := got.Content.ContentBlocks[0]
	if pdf.CacheControl == nil || pdf.CacheControl.Type != schemas.CacheControlTypeEphemeral {
		t.Errorf("pdf missing ephemeral cache_control: %+v", pdf.CacheControl)
	}
}

// TestConvertMessages_TextBlocksHaveNoCacheControl is a guard against
// accidentally over-caching plain text. cache_control on text blocks
// would burn breakpoints without proportional savings (text is cheap)
// and might bump us against the 4-breakpoint ceiling.
func TestConvertMessages_TextBlocksHaveNoCacheControl(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hello"},
			{Type: types.ContentTypeImage, MediaType: "image/png", Data: "AA=="},
		}},
	}
	got := convertMessages(msgs, "", false, nil)
	blocks := got[0].Content.ContentBlocks
	for _, b := range blocks {
		if b.Type == schemas.ChatContentBlockTypeText && b.CacheControl != nil {
			t.Errorf("text block should NOT carry cache_control: %+v", b)
		}
	}
}

// TestConvertMessages_CapsAt4Breakpoints exercises capImageCacheBreakpoints
// — 6 images across history, only the 4 most recent should keep
// cache_control. Anthropic returns 400 if a request exceeds 4
// breakpoints.
func TestConvertMessages_CapsAt4Breakpoints(t *testing.T) {
	mkImageMsg := func(payload string) types.Message {
		return types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeImage, MediaType: "image/png", Data: payload},
			},
		}
	}
	msgs := []types.Message{
		mkImageMsg("a"), mkImageMsg("b"), mkImageMsg("c"),
		mkImageMsg("d"), mkImageMsg("e"), mkImageMsg("f"),
	}
	got := convertMessages(msgs, "", false, nil)

	var imageBlocks []schemas.ChatContentBlock
	for _, m := range got {
		if m.Content == nil || m.Content.ContentBlocks == nil {
			continue
		}
		for _, b := range m.Content.ContentBlocks {
			if b.Type == schemas.ChatContentBlockTypeImage {
				imageBlocks = append(imageBlocks, b)
			}
		}
	}
	if len(imageBlocks) != 6 {
		t.Fatalf("want 6 image blocks, got %d", len(imageBlocks))
	}
	// Oldest 2 should have cache_control cleared.
	for i := 0; i < 2; i++ {
		if imageBlocks[i].CacheControl != nil {
			t.Errorf("oldest image #%d should have CacheControl cleared, got %+v", i, imageBlocks[i].CacheControl)
		}
	}
	// Most recent 4 should keep cache_control.
	for i := 2; i < 6; i++ {
		if imageBlocks[i].CacheControl == nil {
			t.Errorf("recent image #%d lost cache_control", i)
		}
	}
}

func TestConvertMessages_NoCapWhenUnderLimit(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, MediaType: "image/png", Data: "a"},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, MediaType: "image/png", Data: "b"},
		}},
	}
	got := convertMessages(msgs, "", false, nil)
	for _, m := range got {
		if m.Content == nil || m.Content.ContentBlocks == nil {
			continue
		}
		for _, b := range m.Content.ContentBlocks {
			if b.Type == schemas.ChatContentBlockTypeImage && b.CacheControl == nil {
				t.Errorf("≤4 images should all keep cache_control, got nil on %+v", b)
			}
		}
	}
}
