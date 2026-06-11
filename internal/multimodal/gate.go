package multimodal

import (
	"sort"

	"harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/pkg/types"
)

// Gate enforces the active model's SupportsFlags against the typed
// content blocks produced by Build. Returns nil when all blocks are
// acceptable, or *UnsupportedModalityError listing the rejected
// modalities in deterministic alphabetical order.
//
// Caller is responsible for resolving the active model name + supports
// (typically via provider.Manager.ActiveModelKey + registry lookup).
// When fallback-chain intersection gating is enabled, callers pass the
// intersected SupportsFlags here — Gate doesn't know about chains.
func Gate(activeModel string, supports registry.SupportsFlags, blocks []types.ContentBlock) error {
	seen := map[string]bool{}
	for _, b := range blocks {
		modality := modalityOf(b)
		if modality == "" {
			continue
		}
		if !registry.AcceptsModality(supports, modality) {
			seen[modality] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	rejected := make([]string, 0, len(seen))
	for k := range seen {
		rejected = append(rejected, k)
	}
	sort.Strings(rejected)
	return &UnsupportedModalityError{
		Model:              activeModel,
		RejectedModalities: rejected,
	}
}

// modalityOf maps an engine-internal ContentBlock to the AcceptsModality
// token. Text / tool blocks return "" (no gate check needed). Non-PDF
// file blocks also return "" — they currently flow through the legacy
// text-JSON attachment path and are not gated until we add per-file
// capability flags.
//
// Image blocks are deliberately NOT gated: many tools consume images
// (image_generate, video_create image-to-video, browser agent), so a
// non-vision chat model is no longer a reason to reject the message at
// the door. The image passes through and the downstream model/provider
// decides what it can do with it.
func modalityOf(b types.ContentBlock) string {
	switch b.Type {
	case types.ContentTypeFile:
		if b.MediaType == "application/pdf" {
			return "pdf"
		}
		return ""
	}
	return ""
}
