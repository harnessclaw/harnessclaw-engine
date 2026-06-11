package registry

// DeriveCapabilities collapses the granular SupportsFlags matrix into the
// high-level capability buckets used by the UI to render colored chips:
// multimodal / image_generation / tools / reasoning / search.
//
// The fine-grained flags remain authoritative for per-feature gating
// (e.g. the multimodal Gate uses SupportsFlags.Vision directly). The
// derived list is purely a presentation aid — never use it for
// enforcement.
//
// Output order is intentionally stable but not sorted: insertion order
// matches the declaration order of the four buckets, so a caller that
// preserves that order gets a consistent UI layout. Callers that need
// deterministic ordering for comparison should sort.Strings the result.
func DeriveCapabilities(s SupportsFlags) []string {
	var out []string
	if s.Vision || s.PDFInput || s.AudioInput || s.VideoInput {
		out = append(out, "multimodal")
	}
	if s.ImageGeneration {
		out = append(out, "image_generation")
	}
	if s.FunctionCalling {
		out = append(out, "tools")
	}
	if s.Reasoning {
		out = append(out, "reasoning")
	}
	if s.WebSearch {
		out = append(out, "search")
	}
	return out
}

// AcceptsModality reports whether a given user-facing modality token
// (from IncomingContentBlock.Type: "image" / "pdf" / "audio" / "video")
// is permitted by the model's SupportsFlags.
//
// Unknown modality tokens return false — fail-closed so a typo on the
// wire can't accidentally bypass the gate.
func AcceptsModality(s SupportsFlags, modality string) bool {
	switch modality {
	case "image":
		return s.Vision
	case "pdf":
		return s.PDFInput
	case "audio":
		return s.AudioInput
	case "video":
		return s.VideoInput
	}
	return false
}

// KnownModelTypeTokens is the closed set of capability tokens an
// endpoint may declare in its model_type list. Unknown tokens are
// dropped (with a warn) on yaml load and rejected (400) on PATCH.
// Single source of truth; the client mirrors this list verbatim.
var KnownModelTypeTokens = map[string]bool{
	"vision":           true,
	"pdf":              true,
	"audio":            true,
	"video":            true,
	"image_generation": true,
	"reasoning":        true,
	"tools":            true,
	"search":           true,
}

// SupportsFromTokens converts a model_type list to a SupportsFlags
// with only the 7 mapped fields populated. Other fields stay
// zero-valued — callers should merge with the manifest baseline via
// MergeOverride to preserve operational flags (Streaming, etc.).
//
// Unknown tokens are silently ignored at this layer (caller must have
// pre-validated via FilterKnownTokens). Empty input → zero SupportsFlags.
func SupportsFromTokens(tokens []string) SupportsFlags {
	var s SupportsFlags
	for _, t := range tokens {
		switch t {
		case "vision":
			s.Vision = true
		case "pdf":
			s.PDFInput = true
		case "audio":
			s.AudioInput = true
		case "video":
			s.VideoInput = true
		case "image_generation":
			s.ImageGeneration = true
		case "reasoning":
			s.Reasoning = true
		case "tools":
			s.FunctionCalling = true
		case "search":
			s.WebSearch = true
		}
	}
	return s
}

// MergeOverride composes the manifest baseline with an endpoint's
// model_type override. The token-mapped capability fields come
// from the override; all other SupportsFlags fields (Streaming,
// SystemMessages, PromptCaching, etc.) keep the manifest's values
// because those are operational features the user shouldn't have to
// re-declare in their endpoint config.
func MergeOverride(base, override SupportsFlags) SupportsFlags {
	out := base
	out.Vision = override.Vision
	out.PDFInput = override.PDFInput
	out.AudioInput = override.AudioInput
	out.VideoInput = override.VideoInput
	out.ImageGeneration = override.ImageGeneration
	out.Reasoning = override.Reasoning
	out.FunctionCalling = override.FunctionCalling
	out.WebSearch = override.WebSearch
	return out
}

// FilterKnownTokens returns only the recognized tokens (in input
// order) plus the dropped ones. Caller decides whether to warn or
// fail; the returned `known` slice is safe to store as the canonical
// model_type. Returns (nil, nil) for empty input.
func FilterKnownTokens(tokens []string) (known, unknown []string) {
	for _, t := range tokens {
		if KnownModelTypeTokens[t] {
			known = append(known, t)
		} else if t != "" {
			unknown = append(unknown, t)
		}
	}
	return
}
