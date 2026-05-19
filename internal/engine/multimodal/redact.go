package multimodal

import (
	"fmt"

	"harnessclaw-go/pkg/types"
)

// RedactBlocksForLog returns a copy of the content blocks with
// base64 Data fields replaced by a short size summary. Use this
// before logging message structs at Debug level — base64 image
// payloads can run to multiple megabytes and pollute log volumes /
// leak attached document contents if logged verbatim.
//
// The original slice is not mutated. Non-base64 fields (Text, URL,
// Path, MediaType, etc.) are preserved as-is so the log entry is
// still useful for debugging routing and capability decisions.
func RedactBlocksForLog(blocks []types.ContentBlock) []types.ContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]types.ContentBlock, len(blocks))
	for i, b := range blocks {
		out[i] = b
		if (b.Type == types.ContentTypeImage || b.Type == types.ContentTypeFile) && len(b.Data) > 0 {
			out[i].Data = fmt.Sprintf("<base64 redacted; size=%d>", len(b.Data))
		}
	}
	return out
}
