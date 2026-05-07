package emitv2

import (
	"strings"
	"sync"
)

// ArtifactRegistry tracks artifacts produced within a trace so the emit
// Builder can auto-rewrite L1 (persona) text references into protocol
// `artifact://` markdown URIs (v2.2 §11 hard constraint).
//
// Lifecycle: one Registry per trace. The Builder records every
// ArtifactRef it observes (in tool/agent/step payloads), and rewrites
// matching name occurrences in subsequent ChannelText appends.
//
// Why this lives in the emit package: §11 calls it a "framework-side
// hard constraint, prompt-drift-immune". Putting the rewrite in the
// Builder (vs in a downstream renderer) is the only way to keep it
// uniform across L1 personas — by the time text reaches the wire, it's
// already URI-fied.
type ArtifactRegistry struct {
	mu  sync.RWMutex
	byName map[string]string // name → artifact_id
}

// NewArtifactRegistry constructs an empty registry.
func NewArtifactRegistry() *ArtifactRegistry {
	return &ArtifactRegistry{byName: make(map[string]string)}
}

// Record adds an artifact's (name → id) mapping to the registry. Called
// by the Builder when it observes ArtifactRef on tool/agent/step
// payloads. Subsequent occurrences of name in ChannelText appends will
// be rewritten.
//
// Conflict policy: when two artifacts share a name, the most-recent
// Record wins. The renderer can still disambiguate via the wire
// payload's ArtifactRef list, so this is acceptable.
func (r *ArtifactRegistry) Record(name, id string) {
	if name == "" || id == "" {
		return
	}
	r.mu.Lock()
	r.byName[name] = id
	r.mu.Unlock()
}

// RecordRefs records every artifact in refs.
func (r *ArtifactRegistry) RecordRefs(refs []ArtifactRef) {
	for _, a := range refs {
		r.Record(a.Name, a.ArtifactID)
	}
}

// Lookup returns the id for name, if known.
func (r *ArtifactRegistry) Lookup(name string) (string, bool) {
	r.mu.RLock()
	id, ok := r.byName[name]
	r.mu.RUnlock()
	return id, ok
}

// Names returns a snapshot of all currently registered names. Used by
// the rewriter to know what to look for. Returned slice is a copy.
func (r *ArtifactRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	return names
}

// Rewrite scans text for known artifact names and replaces them with
// markdown URI links — `name` becomes `[name](artifact://id)`. Skips
// occurrences that are already inside a markdown link to avoid double-
// wrapping (e.g. `[name](artifact://x)` stays untouched).
//
// Rules:
//   - Match longest names first (so a name that is a substring of
//     another doesn't get rewritten as the shorter one).
//   - Skip names already followed by `](artifact://` (already rewritten).
//   - Skip names appearing inside `[...]` brackets followed by `(...)`
//     (already inside another markdown link).
//   - Preserve the original casing of the name.
//
// Performance: O(text * names). For typical text chunks (≤ 500 chars)
// and registries (≤ 20 artifacts) this is well under 10µs and safe to
// run on the hot path of card.append.
func (r *ArtifactRegistry) Rewrite(text string) string {
	if text == "" {
		return text
	}
	r.mu.RLock()
	if len(r.byName) == 0 {
		r.mu.RUnlock()
		return text
	}
	// Snapshot under lock; release before string scan.
	pairs := make([]artifactPair, 0, len(r.byName))
	for name, id := range r.byName {
		pairs = append(pairs, artifactPair{name: name, id: id})
	}
	r.mu.RUnlock()

	// Sort longest-name-first so we never under-match.
	sortByNameLengthDesc(pairs)

	out := text
	for _, p := range pairs {
		out = rewriteOne(out, p.name, p.id)
	}
	return out
}

type artifactPair struct {
	name string
	id   string
}

func sortByNameLengthDesc(pairs []artifactPair) {
	// Insertion sort — pairs is small (typically ≤ 10).
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && len(pairs[j-1].name) < len(pairs[j].name); j-- {
			pairs[j-1], pairs[j] = pairs[j], pairs[j-1]
		}
	}
}

// rewriteOne replaces every "free" occurrence of name with markdown URI
// in s. Free = not already inside a markdown link, not already wrapped.
func rewriteOne(s, name, id string) string {
	if !strings.Contains(s, name) {
		return s
	}
	target := "[" + name + "](artifact://" + id + ")"
	var out strings.Builder
	out.Grow(len(s) + len(target))
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], name)
		if idx < 0 {
			out.WriteString(s[i:])
			break
		}
		absStart := i + idx
		absEnd := absStart + len(name)

		// Skip if already inside a markdown link's display text:
		// when name is preceded by '[' and followed by '](', it's the
		// target of a markdown link and we keep it as-is — but we still
		// have to advance past it.
		if absStart > 0 && s[absStart-1] == '[' &&
			absEnd+1 < len(s) && s[absEnd] == ']' && s[absEnd+1] == '(' {
			// Find the closing ')' of this link and skip past it.
			closeIdx := strings.IndexByte(s[absEnd+1:], ')')
			if closeIdx >= 0 {
				out.WriteString(s[i : absEnd+1+closeIdx+1])
				i = absEnd + 1 + closeIdx + 1
				continue
			}
		}

		// Skip if name is part of a longer identifier (alphanumeric on
		// either side). Prevents "foo.md" matching inside "myfoo.md".
		if absStart > 0 && isWordChar(s[absStart-1]) {
			out.WriteString(s[i : absEnd])
			i = absEnd
			continue
		}
		if absEnd < len(s) && isWordChar(s[absEnd]) {
			out.WriteString(s[i : absEnd])
			i = absEnd
			continue
		}

		out.WriteString(s[i:absStart])
		out.WriteString(target)
		i = absEnd
	}
	return out.String()
}

// isWordChar reports whether c is part of an identifier-like token for
// the purpose of "name boundary" detection. Includes filename-friendly
// characters (-, ., /) so that names like "annual-report.md" are
// treated as a single token and "report.md" doesn't match as a
// sub-token of the longer name.
func isWordChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_', c == '-', c == '.', c == '/':
		return true
	}
	return false
}
