// Package multimodal handles normalization and capability-gating of
// non-text user content (images, PDFs, files) before it reaches the
// LLM adapter.
//
// Responsibilities split:
//   - builder.go normalizes IncomingContentBlock[] → []types.ContentBlock
//     with format/MIME/size validation
//   - gate.go enforces SupportsFlags against the active agent's model
//   - errors.go typed errors with user-facing messages (Chinese)
//   - redact.go base64-safe log helpers
package multimodal

import "fmt"

// UnsupportedModalityError is returned by Gate when one or more
// incoming content blocks request a modality the active model can't
// handle. It carries enough structured data for the channel layer to
// render a clear error frame (model name + rejected modality list).
type UnsupportedModalityError struct {
	// Model is the provider:endpoint manifest key of the offending
	// model (e.g. "anthropic:claude-haiku-4-5"). When fallback-chain
	// intersection gating is in use, this may be the chain entry that
	// caused the rejection rather than the primary.
	Model string

	// RejectedModalities lists modality tokens the model can't accept,
	// deduplicated and sorted alphabetically for deterministic output.
	RejectedModalities []string
}

func (e *UnsupportedModalityError) Error() string {
	return fmt.Sprintf("model %s does not support modalities %v", e.Model, e.RejectedModalities)
}

// UserMessage returns Chinese-language user-facing copy. Embedded in
// the `error.user_message` field of the wire frame so the client can
// surface it verbatim without code-side translation tables.
func (e *UnsupportedModalityError) UserMessage() string {
	if len(e.RejectedModalities) == 0 {
		return "当前模型不支持该输入"
	}
	mod := "图片"
	switch e.RejectedModalities[0] {
	case "pdf":
		mod = "PDF"
	case "audio":
		mod = "音频"
	case "video":
		mod = "视频"
	}
	return fmt.Sprintf("当前模型不支持%s输入，请切换到具备多模态能力的模型后重试。", mod)
}

// ValidationError is returned by Builder when an incoming block is
// malformed (missing media_type, oversized, unsupported type, etc.).
// Surfaced as invalid_input on the wire; distinct from
// UnsupportedModalityError, which is a capability mismatch on
// otherwise-well-formed input.
type ValidationError struct {
	// Reason is the developer-facing message.
	Reason string
	// Index is the position of the offending block in the
	// IncomingMessage.Content array. -1 when not applicable.
	Index int
}

func (e *ValidationError) Error() string {
	if e.Index < 0 {
		return e.Reason
	}
	return fmt.Sprintf("content[%d]: %s", e.Index, e.Reason)
}
