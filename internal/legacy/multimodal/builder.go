package multimodal

import (
	"harnessclaw-go/pkg/types"
)

// MaxBase64BlockBytes caps a single base64-encoded inline payload. Picked
// to keep a single user.message frame under the WebSocket default
// fragment ceiling (~10 MB encoded ≈ 7.5 MB raw). Override only if you
// also lift the channel-level frame cap.
const MaxBase64BlockBytes = 10 * 1024 * 1024

// MaxTotalBytesPerMessage caps the sum of base64 data across all
// content blocks of one user.message. Defense against many-small-images
// flooding context. ~20 MB encoded ≈ 15 MB raw.
const MaxTotalBytesPerMessage = 20 * 1024 * 1024

// AllowedImageMIMEs is the closed set of image MIME types we forward
// to providers. SVG is deliberately excluded — it can carry
// executable scripts and most LLM vision models don't render it
// usefully anyway. Anthropic / OpenAI Vision both restrict to these
// four formats, so matching their inputs avoids ambiguous upstream
// errors. Keep this list in sync with the desktop client's IPC
// whitelist in src/main/index.ts (sniffMimeForBase64).
var AllowedImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// AllowedPDFMIMEs covers the document path. application/pdf is the
// only document MIME bifrost's anthropic provider currently
// transcodes — other formats (.docx / .xlsx) go through the legacy
// JSON-text-attachment route, not as native multimodal blocks.
var AllowedPDFMIMEs = map[string]bool{
	"application/pdf": true,
}

// Build normalizes an IncomingMessage's Text + Content[] into the
// engine-internal []types.ContentBlock shape.
//
// Rules:
//   - If Content is empty: produce a single text block from Text (back-compat
//     with the legacy v1 wire path that didn't carry a Content array).
//   - If Content is non-empty: each block is validated and converted in
//     order; the top-level Text is ignored (the channel adapter has
//     already concatenated text blocks back into Text for legacy v1
//     consumers; we trust the typed array here).
//   - Empty text blocks are dropped silently (clients sometimes attach
//     an empty trailing text block when only an image is sent).
//
// Returns *ValidationError on the first malformed block. Never returns
// *UnsupportedModalityError — capability gating is a separate concern,
// see Gate.
func Build(text string, content []types.IncomingContentBlock) ([]types.ContentBlock, error) {
	if len(content) == 0 {
		return []types.ContentBlock{{Type: types.ContentTypeText, Text: text}}, nil
	}

	out := make([]types.ContentBlock, 0, len(content))
	totalBytes := 0
	for i, in := range content {
		switch in.Type {
		case "text":
			if in.Text == "" {
				continue
			}
			out = append(out, types.ContentBlock{Type: types.ContentTypeText, Text: in.Text})

		case "image", "pdf":
			block, n, err := normalizeMediaBlock(i, in)
			if err != nil {
				return nil, err
			}
			totalBytes += n
			if totalBytes > MaxTotalBytesPerMessage {
				return nil, &ValidationError{Index: i, Reason: "total inline payload exceeds 20MB cap"}
			}
			out = append(out, block)

		default:
			return nil, &ValidationError{
				Index:  i,
				Reason: "unsupported content type: " + in.Type,
			}
		}
	}
	return out, nil
}

// normalizeMediaBlock handles image / pdf entries. Returns the encoded
// payload size (in base64 chars) for cumulative-size accounting.
func normalizeMediaBlock(idx int, in types.IncomingContentBlock) (types.ContentBlock, int, error) {
	if in.MIMEType == "" {
		return types.ContentBlock{}, 0, &ValidationError{Index: idx, Reason: "media_type is required for " + in.Type}
	}
	// MIME whitelist — closed set per allowed{Image,PDF}MIMEs. Rejects
	// image/svg+xml (script-bearing) and any format the upstream
	// providers wouldn't understand. Mirror of the client-side
	// IPC whitelist; both sides must stay aligned.
	allowed := false
	switch in.Type {
	case "image":
		allowed = AllowedImageMIMEs[in.MIMEType]
	case "pdf":
		allowed = AllowedPDFMIMEs[in.MIMEType]
	}
	if !allowed {
		return types.ContentBlock{}, 0, &ValidationError{
			Index:  idx,
			Reason: "media_type not in whitelist: " + in.MIMEType,
		}
	}
	if in.Data == "" && in.URL == "" && in.Path == "" {
		return types.ContentBlock{}, 0, &ValidationError{Index: idx, Reason: in.Type + " source must include data, url, or path"}
	}
	if len(in.Data) > MaxBase64BlockBytes {
		return types.ContentBlock{}, 0, &ValidationError{
			Index:  idx,
			Reason: "inline base64 data exceeds 10MB cap; use URL source or downsize",
		}
	}

	t := types.ContentTypeImage
	if in.Type == "pdf" {
		t = types.ContentTypeFile
	}
	return types.ContentBlock{
		Type:      t,
		MediaType: in.MIMEType,
		Data:      in.Data,
		URL:       in.URL,
		Path:      in.Path,
	}, len(in.Data), nil
}
