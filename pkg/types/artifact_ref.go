package types

// ArtifactRef is the lightweight wire representation of an artifact —
// what events carry to clients so the UI can render "this task produced
// X" cards without ever loading the artifact's full content.
//
// JSON shape mirrors the artifact design doc §10 verbatim (so the wire
// protocol matches the spec). The internal-side artifact.Ref struct is
// the same conceptually but lives in internal/artifact; we duplicate
// the shape here to avoid pkg/types ever importing internal/* (which
// would break the `pkg = public, internal = private` boundary).
//
// Producers fill what they have — only ArtifactID is strictly required;
// every other field is optional so producers that have only the ID
// (e.g. when forwarding a foreign artifact) can still emit a usable Ref.
type ArtifactRef struct {
	// ArtifactID is the canonical identifier ("art_<24 hex>"). Required.
	ArtifactID string `json:"artifact_id"`

	// Name is a short human-readable label (e.g. "sales-2024.md").
	Name string `json:"name,omitempty"`

	// Type classifies the payload: "structured" | "file" | "blob".
	// Stringified rather than enum-typed so this struct stays
	// import-free; the artifact package owns the canonical Type enum.
	Type string `json:"type,omitempty"`

	// MIMEType is the content type when known (e.g. "text/markdown").
	MIMEType string `json:"mime_type,omitempty"`

	// SizeBytes is the byte length of the underlying content. Helps the
	// client decide whether to inline-preview or offer download.
	SizeBytes int `json:"size_bytes,omitempty"`

	// Description is the producer's one-line "what is this".
	Description string `json:"description,omitempty"`

	// PreviewText is the truncated text-form preview the LLM would see
	// in mode=metadata/preview. Optional; clients may render it as a
	// snippet under the artifact card.
	PreviewText string `json:"preview_text,omitempty"`

	// URI is the addressable handle (e.g. "artifact://art_xxx"). Lets
	// the client construct a download / fetch action without hard-coding
	// the protocol.
	URI string `json:"uri,omitempty"`

	// Role binds the artifact to a role declared in the task's
	// ExpectedOutputs. The L3 sets this on SubmitTaskResult so the
	// framework can match each delivered artifact against its contract
	// (type, schema, min size). Empty when the task had no contract.
	Role string `json:"role,omitempty"`
}
