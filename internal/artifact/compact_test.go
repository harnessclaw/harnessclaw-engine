package artifact

import (
	"strings"
	"testing"
	"time"

	"harnessclaw-go/pkg/types"
)

// --- CompactMessages tests ---

func TestCompactMessagesNilState(t *testing.T) {
	msgs := []types.Message{{Role: types.RoleUser}}
	result := CompactMessages(msgs, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestCompactMessagesNoReplacements(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "") // kept

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: "full content"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if result[0].Content[0].ToolResult != "full content" {
		t.Error("kept tool_result should not be modified")
	}
}

func TestCompactMessagesWithReplacement(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "[Artifact art_abc: summary (5000 chars)]")

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: strings.Repeat("x", 5000)},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if result[0].Content[0].ToolResult != "[Artifact art_abc: summary (5000 chars)]" {
		t.Errorf("tool_result = %q, want replacement text", result[0].Content[0].ToolResult)
	}
}

func TestCompactMessagesDoesNotMutateOriginal(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "replacement")

	original := "original content"
	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: original},
			},
		},
	}

	_ = CompactMessages(msgs, rs)

	// Original should be unchanged.
	if msgs[0].Content[0].ToolResult != original {
		t.Error("CompactMessages should not mutate the original messages")
	}
}

func TestCompactMessagesMixedBlocks(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "ref1")
	rs.Decide("tu_2", "")    // kept
	rs.Decide("tu_3", "ref3")

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: "big1"},
				{Type: types.ContentTypeText, Text: "some text"},
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_2", ToolResult: "small"},
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_3", ToolResult: "big3"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	blocks := result[0].Content

	if blocks[0].ToolResult != "ref1" {
		t.Errorf("block[0] = %q, want ref1", blocks[0].ToolResult)
	}
	if blocks[1].Text != "some text" {
		t.Errorf("block[1] text = %q, want 'some text'", blocks[1].Text)
	}
	if blocks[2].ToolResult != "small" {
		t.Errorf("block[2] = %q, want 'small' (kept)", blocks[2].ToolResult)
	}
	if blocks[3].ToolResult != "ref3" {
		t.Errorf("block[3] = %q, want ref3", blocks[3].ToolResult)
	}
}

func TestCompactMessagesMultipleMessages(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "ref1")

	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "I'll help"},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: "big content"},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "thanks"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Content[0].Text != "I'll help" {
		t.Error("assistant message should be unchanged")
	}
	if result[1].Content[0].ToolResult != "ref1" {
		t.Error("tool_result should be replaced")
	}
	if result[2].Content[0].Text != "thanks" {
		t.Error("text message should be unchanged")
	}
}

func TestCompactMessagesUnseenToolUseID(t *testing.T) {
	rs := NewReplacementState()
	// tu_unseen is not in the replacement state at all.

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_unseen", ToolResult: "content"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if result[0].Content[0].ToolResult != "content" {
		t.Error("unseen tool_use_id should not be modified")
	}
}

func TestCompactMessagesEmptyToolUseID(t *testing.T) {
	rs := NewReplacementState()

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "", ToolResult: "content"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if result[0].Content[0].ToolResult != "content" {
		t.Error("empty tool_use_id should not be modified")
	}
}

func TestCompactMessagesPreservesMessageFields(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "ref1")

	now := time.Now()
	msgs := []types.Message{
		{
			ID:        "msg_1",
			Role:      types.RoleUser,
			CreatedAt: now,
			Tokens:    42,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: "big"},
			},
		},
	}

	result := CompactMessages(msgs, rs)
	if result[0].ID != "msg_1" {
		t.Errorf("ID = %q, want msg_1", result[0].ID)
	}
	if result[0].Role != types.RoleUser {
		t.Errorf("Role = %q, want user", result[0].Role)
	}
	if !result[0].CreatedAt.Equal(now) {
		t.Error("CreatedAt should be preserved")
	}
	if result[0].Tokens != 42 {
		t.Errorf("Tokens = %d, want 42", result[0].Tokens)
	}
}

// --- PersistAndReplace tests ---

func TestPersistAndReplaceSmallContent(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	content, artID := PersistAndReplace(store, rs, "tu_1", "Bash", "small output", false, nil, 1000, 0)

	if content != "small output" {
		t.Errorf("content = %q, want original", content)
	}
	if artID != "" {
		t.Errorf("artID = %q, want empty", artID)
	}
	if store.Len() != 0 {
		t.Error("store should be empty for small content")
	}
	if !rs.IsSeen("tu_1") {
		t.Error("tu_1 should be marked as seen (kept)")
	}
	if _, ok := rs.IsReplaced("tu_1"); ok {
		t.Error("tu_1 should not be marked as replaced")
	}
}

func TestPersistAndReplaceLargeContent(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	bigContent := strings.Repeat("x", 5000)
	content, artID := PersistAndReplace(store, rs, "tu_1", "FileRead", bigContent, false, nil, 1000, 200)

	if artID == "" {
		t.Fatal("artID should not be empty for large content")
	}
	if !strings.HasPrefix(artID, "art_") {
		t.Errorf("artID should have art_ prefix, got %q", artID)
	}
	if content == bigContent {
		t.Error("content should be replaced with preview, not original")
	}
	if !strings.Contains(content, "truncated") {
		t.Error("preview should mention truncation")
	}
	if !strings.Contains(content, artID) {
		t.Error("preview should contain the artifact ID")
	}
	if store.Len() != 1 {
		t.Errorf("store should have 1 artifact, got %d", store.Len())
	}

	// Verify full content is in store.
	art := store.Get(artID)
	if art.Content != bigContent {
		t.Error("stored artifact content should be the full original")
	}

	// Verify replacement state.
	if !rs.IsSeen("tu_1") {
		t.Error("tu_1 should be seen")
	}
	if _, ok := rs.IsReplaced("tu_1"); !ok {
		t.Error("tu_1 should be marked as replaced")
	}
}

func TestPersistAndReplaceError(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	bigError := strings.Repeat("E", 5000)
	content, artID := PersistAndReplace(store, rs, "tu_1", "Bash", bigError, true, nil, 1000, 0)

	if content != bigError {
		t.Error("error content should never be replaced")
	}
	if artID != "" {
		t.Error("error should not produce an artifact ID")
	}
	if store.Len() != 0 {
		t.Error("errors should not be stored as artifacts")
	}
}

func TestPersistAndReplaceWithMetadata(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	meta := map[string]any{"path": "/tmp/file.txt"}
	bigContent := strings.Repeat("y", 5000)
	_, artID := PersistAndReplace(store, rs, "tu_1", "FileRead", bigContent, false, meta, 1000, 0)

	art := store.Get(artID)
	if art.Metadata["path"] != "/tmp/file.txt" {
		t.Error("metadata should be preserved in artifact")
	}
}

func TestPersistAndReplaceDefaultThreshold(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	// Content just below DefaultThreshold (4096).
	small := strings.Repeat("a", DefaultThreshold-1)
	content, artID := PersistAndReplace(store, rs, "tu_1", "Bash", small, false, nil, 0, 0)
	if artID != "" {
		t.Error("content below default threshold should not be persisted")
	}
	if content != small {
		t.Error("content should be unchanged")
	}

	// Content at exactly DefaultThreshold — still below (< not <=).
	exact := strings.Repeat("b", DefaultThreshold)
	_, artID2 := PersistAndReplace(store, rs, "tu_2", "Bash", exact, false, nil, 0, 0)
	if artID2 == "" {
		t.Error("content at exact threshold should be persisted")
	}

	// Content above DefaultThreshold.
	big := strings.Repeat("c", DefaultThreshold+1)
	_, artID3 := PersistAndReplace(store, rs, "tu_3", "Bash", big, false, nil, 0, 0)
	if artID3 == "" {
		t.Error("content above default threshold should be persisted")
	}
}

// --- Integration: PersistAndReplace + CompactMessages ---

func TestPersistThenCompact(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	// Simulate tool execution: one large, one small.
	bigContent := strings.Repeat("x", 5000)
	preview1, artID1 := PersistAndReplace(store, rs, "tu_1", "FileRead", bigContent, false, nil, 1000, 100)
	content2, _ := PersistAndReplace(store, rs, "tu_2", "Bash", "ls output", false, nil, 1000, 0)

	// Build messages as the query loop would.
	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: preview1},
			},
		},
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "I see the file content"},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_2", ToolResult: content2},
			},
		},
	}

	// CompactMessages should produce same result (messages already have preview).
	compacted := CompactMessages(msgs, rs)

	// tu_1 should have the preview (already replaced at persist time).
	if compacted[0].Content[0].ToolResult != preview1 {
		t.Error("tu_1 should have preview content")
	}
	// tu_2 should be unchanged.
	if compacted[2].Content[0].ToolResult != "ls output" {
		t.Error("tu_2 should be unchanged")
	}

	// Verify full content is still retrievable from store.
	art := store.Get(artID1)
	if art == nil || art.Content != bigContent {
		t.Error("full content should still be in store")
	}
}

func TestPersistThenCompactFrozenDecisions(t *testing.T) {
	store := NewStore()
	rs := NewReplacementState()

	// First turn: persist a large result.
	big := strings.Repeat("z", 5000)
	preview, _ := PersistAndReplace(store, rs, "tu_1", "FileRead", big, false, nil, 1000, 100)

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu_1", ToolResult: preview},
			},
		},
	}

	// Compact multiple times — result should be identical (frozen).
	for i := 0; i < 5; i++ {
		compacted := CompactMessages(msgs, rs)
		if compacted[0].Content[0].ToolResult != preview {
			t.Errorf("iteration %d: content changed, expected frozen preview", i)
		}
	}
}
