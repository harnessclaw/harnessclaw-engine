package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// idPrefix prefixes every artifact ID so they're recognisable in logs and
// the LLM can be trained to validate the shape before passing one around.
const idPrefix = "art_"

// NewID returns a fresh random artifact ID. Always generated server-side
// (doc §12 — never let the LLM fabricate IDs).
func NewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal in any sane runtime — surface it
		// loudly rather than returning a degenerate ID that would silently
		// collide.
		panic(fmt.Errorf("artifact: crypto/rand failed: %w", err))
	}
	return idPrefix + hex.EncodeToString(b[:])
}

// IsValidID checks that s looks like an ID we issued. Used by Read to
// reject obvious LLM hallucinations early with a clearer error than "not
// found".
func IsValidID(s string) bool {
	if len(s) != len(idPrefix)+24 {
		return false
	}
	if s[:len(idPrefix)] != idPrefix {
		return false
	}
	for _, c := range s[len(idPrefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
