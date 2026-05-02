package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// DefaultPreviewBytes is the default upper bound on the inline preview
// stored on each artifact. Doc §4 calls for a "first 200 chars" preview;
// 512 bytes accommodates Chinese (3 bytes/rune) and JSON snippets without
// blowing token budgets when the Ref ships in tool_result.
const DefaultPreviewBytes = 512

// MakePreview returns a UTF-8-safe truncation of content for the preview
// field. Truncating mid-codepoint produces \xe5 garbage that confuses
// downstream consumers, so we cut at a rune boundary.
func MakePreview(content string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(content) <= maxBytes {
		return content
	}
	// Find the rune boundary <= maxBytes.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return strings.TrimRightFunc(content[:cut], func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}) + "…"
}

// Checksum returns "sha256:<hex>" for the given content. Stored on every
// artifact so consumers can detect silent corruption — especially useful
// when artifacts move across storage backends.
func Checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
