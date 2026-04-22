package artifact

import (
	"harnessclaw-go/pkg/types"
)

// CompactMessages processes a slice of messages, replacing tool_result
// content with artifact references for tool_use_ids that have been marked
// as replaced in the ReplacementState.
//
// The function returns a new slice — the original messages are not modified.
// Only ContentBlocks of type tool_result with a matching replacement entry
// are affected; all other blocks pass through unchanged.
//
// This should be called after sess.GetMessages() and before provider.Chat()
// to reduce the input token count sent to the LLM.
func CompactMessages(messages []types.Message, rs *ReplacementState) []types.Message {
	if rs == nil {
		return messages
	}

	result := make([]types.Message, len(messages))
	for i, msg := range messages {
		result[i] = compactMessage(msg, rs)
	}
	return result
}

// compactMessage returns a copy of msg with tool_result content replaced
// according to the ReplacementState. Non-tool-result blocks are unchanged.
func compactMessage(msg types.Message, rs *ReplacementState) types.Message {
	if len(msg.Content) == 0 {
		return msg
	}

	// Check if any block needs replacement.
	needsCopy := false
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
			if replacement, ok := rs.IsReplaced(cb.ToolUseID); ok && replacement != "" {
				needsCopy = true
				break
			}
		}
	}
	if !needsCopy {
		return msg
	}

	// Copy the message and replace relevant blocks.
	cp := msg
	cp.Content = make([]types.ContentBlock, len(msg.Content))
	for j, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
			if replacement, ok := rs.IsReplaced(cb.ToolUseID); ok && replacement != "" {
				cb.ToolResult = replacement
			}
		}
		cp.Content[j] = cb
	}
	return cp
}

// PersistAndReplace checks if a tool result is large enough to persist as
// an artifact. If so, it saves the content, records the replacement decision,
// and returns the preview text and artifact ID. Otherwise it records a "keep"
// decision and returns the original content unchanged.
//
// This is the main entry point used by the query loop after tool execution.
func PersistAndReplace(
	store *Store,
	rs *ReplacementState,
	toolUseID string,
	toolName string,
	content string,
	isError bool,
	meta map[string]any,
	threshold int,
	previewLen int,
) (resultContent string, artifactID string) {
	// Errors are never persisted — they are typically short and the LLM
	// needs to see them in full to diagnose and recover.
	if isError {
		rs.Decide(toolUseID, "")
		return content, ""
	}

	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	if previewLen <= 0 {
		previewLen = DefaultPreviewLen
	}

	if len(content) < threshold {
		rs.Decide(toolUseID, "")
		return content, ""
	}

	// Persist and replace.
	id := store.Save(toolUseID, toolName, content, meta)
	preview := store.Preview(id, previewLen)
	rs.Decide(toolUseID, preview)
	return preview, id
}
