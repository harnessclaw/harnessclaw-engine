package emma

import (
	"strings"

	"harnessclaw-go/pkg/types"
)

// TruncateForDisplay clips a string to n runes for safe inclusion in a
// Display.Summary or .Title field. The "…" suffix signals truncation.
func TruncateForDisplay(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ExtractMessageText extracts the text content from a message's content blocks.
func ExtractMessageText(msg *types.Message) string {
	var buf strings.Builder
	for _, block := range msg.Content {
		if block.Type == types.ContentTypeText {
			buf.WriteString(block.Text)
		}
	}
	return buf.String()
}
