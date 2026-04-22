package artifact

import (
	"strings"
	"sync"
	"testing"
)

func TestSaveAndGet(t *testing.T) {
	s := NewStore()

	id := s.Save("tu_1", "FileRead", "hello world", nil)

	if !strings.HasPrefix(id, "art_") {
		t.Fatalf("expected id prefix art_, got %q", id)
	}

	a := s.Get(id)
	if a == nil {
		t.Fatal("expected artifact, got nil")
	}
	if a.Content != "hello world" {
		t.Errorf("content = %q, want %q", a.Content, "hello world")
	}
	if a.ToolUseID != "tu_1" {
		t.Errorf("tool_use_id = %q, want %q", a.ToolUseID, "tu_1")
	}
	if a.ToolName != "FileRead" {
		t.Errorf("tool_name = %q, want %q", a.ToolName, "FileRead")
	}
	if a.Size != 11 {
		t.Errorf("size = %d, want 11", a.Size)
	}
	if a.CreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}
}

func TestGetNotFound(t *testing.T) {
	s := NewStore()
	if a := s.Get("art_nonexistent"); a != nil {
		t.Errorf("expected nil, got %+v", a)
	}
}

func TestGetByToolUse(t *testing.T) {
	s := NewStore()
	id := s.Save("tu_42", "Bash", "output data", nil)

	a := s.GetByToolUse("tu_42")
	if a == nil {
		t.Fatal("expected artifact by tool_use_id, got nil")
	}
	if a.ID != id {
		t.Errorf("id = %q, want %q", a.ID, id)
	}

	// Non-existent tool_use_id.
	if a := s.GetByToolUse("tu_999"); a != nil {
		t.Errorf("expected nil for unknown tool_use_id, got %+v", a)
	}
}

func TestGetByToolUseEmptyID(t *testing.T) {
	s := NewStore()
	// Save with empty tool_use_id should still work but not be indexed.
	s.Save("", "Bash", "data", nil)

	if a := s.GetByToolUse(""); a != nil {
		t.Errorf("expected nil for empty tool_use_id lookup, got %+v", a)
	}
}

func TestSaveWithMetadata(t *testing.T) {
	s := NewStore()
	meta := map[string]any{"path": "/tmp/file.txt", "size": 1024}
	id := s.Save("tu_1", "FileWrite", "content", meta)

	a := s.Get(id)
	if a.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if a.Metadata["path"] != "/tmp/file.txt" {
		t.Errorf("metadata[path] = %v, want /tmp/file.txt", a.Metadata["path"])
	}
}

func TestSummaryTruncation(t *testing.T) {
	s := NewStore()
	// Content longer than 200 characters.
	longContent := strings.Repeat("a", 500)
	id := s.Save("tu_1", "Bash", longContent, nil)

	a := s.Get(id)
	if len([]rune(a.Summary)) != 200 {
		t.Errorf("summary length = %d, want 200", len([]rune(a.Summary)))
	}
}

func TestSummaryShortContent(t *testing.T) {
	s := NewStore()
	id := s.Save("tu_1", "Bash", "short", nil)

	a := s.Get(id)
	if a.Summary != "short" {
		t.Errorf("summary = %q, want %q", a.Summary, "short")
	}
}

func TestRef(t *testing.T) {
	s := NewStore()
	id := s.Save("tu_1", "Bash", "hello world", nil)

	ref := s.Ref(id)
	if !strings.Contains(ref, id) {
		t.Errorf("ref should contain artifact id %q, got %q", id, ref)
	}
	if !strings.Contains(ref, "11 chars") {
		t.Errorf("ref should contain size, got %q", ref)
	}
}

func TestRefNotFound(t *testing.T) {
	s := NewStore()
	if ref := s.Ref("art_nonexistent"); ref != "" {
		t.Errorf("expected empty ref for unknown id, got %q", ref)
	}
}

func TestPreview(t *testing.T) {
	s := NewStore()
	longContent := strings.Repeat("x", 5000)
	id := s.Save("tu_1", "Bash", longContent, nil)

	preview := s.Preview(id, 100)
	if !strings.HasPrefix(preview, strings.Repeat("x", 100)) {
		t.Error("preview should start with the first 100 chars of content")
	}
	if !strings.Contains(preview, "truncated") {
		t.Error("preview should contain truncated notice")
	}
	if !strings.Contains(preview, id) {
		t.Error("preview should contain artifact id")
	}
	if !strings.Contains(preview, "5000 chars") {
		t.Error("preview should contain original size")
	}
}

func TestPreviewShortContent(t *testing.T) {
	s := NewStore()
	id := s.Save("tu_1", "Bash", "short content", nil)

	preview := s.Preview(id, 100)
	if preview != "short content" {
		t.Errorf("preview = %q, want %q", preview, "short content")
	}
}

func TestPreviewDefaultLen(t *testing.T) {
	s := NewStore()
	longContent := strings.Repeat("y", 5000)
	id := s.Save("tu_1", "Bash", longContent, nil)

	// maxLen=0 should use DefaultPreviewLen.
	preview := s.Preview(id, 0)
	if !strings.HasPrefix(preview, strings.Repeat("y", DefaultPreviewLen)) {
		t.Error("preview with maxLen=0 should use DefaultPreviewLen")
	}
}

func TestPreviewNotFound(t *testing.T) {
	s := NewStore()
	if preview := s.Preview("art_nonexistent", 100); preview != "" {
		t.Errorf("expected empty preview for unknown id, got %q", preview)
	}
}

func TestList(t *testing.T) {
	s := NewStore()
	s.Save("tu_1", "A", "aaa", nil)
	s.Save("tu_2", "B", "bbb", nil)
	s.Save("tu_3", "C", "ccc", nil)

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("list length = %d, want 3", len(list))
	}
}

func TestLen(t *testing.T) {
	s := NewStore()
	if s.Len() != 0 {
		t.Errorf("empty store Len() = %d, want 0", s.Len())
	}
	s.Save("tu_1", "A", "aaa", nil)
	s.Save("tu_2", "B", "bbb", nil)
	if s.Len() != 2 {
		t.Errorf("Len() = %d, want 2", s.Len())
	}
}

func TestTotalSize(t *testing.T) {
	s := NewStore()
	s.Save("tu_1", "A", "aaa", nil)    // 3 bytes
	s.Save("tu_2", "B", "bbbbbb", nil) // 6 bytes

	if total := s.TotalSize(); total != 9 {
		t.Errorf("TotalSize() = %d, want 9", total)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s := NewStore()
	id := s.Save("tu_1", "A", "original", nil)

	a1 := s.Get(id)
	a1.Content = "modified"

	a2 := s.Get(id)
	if a2.Content != "original" {
		t.Error("Get should return a copy; modifying the copy should not affect the store")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	// Concurrent writes.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Save("tu_concurrent", "Tool", strings.Repeat("x", 100), nil)
		}(i)
	}

	// Concurrent reads.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.List()
			s.Len()
			s.TotalSize()
		}()
	}

	wg.Wait()

	if s.Len() != 100 {
		t.Errorf("after 100 concurrent saves, Len() = %d, want 100", s.Len())
	}
}

func TestUniqueIDs(t *testing.T) {
	s := NewStore()
	ids := make(map[string]bool)

	for i := 0; i < 1000; i++ {
		id := s.Save("", "Tool", "data", nil)
		if ids[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		ids[id] = true
	}
}

func TestTruncateUnicode(t *testing.T) {
	// Ensure truncation is rune-safe.
	input := "你好世界Hello" // 9 runes
	result := truncate(input, 4)
	if result != "你好世界" {
		t.Errorf("truncate = %q, want %q", result, "你好世界")
	}
}

func TestTruncateNoOp(t *testing.T) {
	input := "short"
	result := truncate(input, 100)
	if result != input {
		t.Errorf("truncate = %q, want %q", result, input)
	}
}

func TestRestore(t *testing.T) {
	s := NewStore()

	art := &Artifact{
		ID:        "art_restored1",
		ToolUseID: "tu_r1",
		ToolName:  "Read",
		Content:   "restored content",
		Summary:   "restored cont...",
		Size:      16,
	}
	s.Restore(art)

	got := s.Get("art_restored1")
	if got == nil {
		t.Fatal("restored artifact not found")
	}
	if got.Content != "restored content" {
		t.Errorf("content = %q, want 'restored content'", got.Content)
	}
	if got.ToolUseID != "tu_r1" {
		t.Errorf("tool_use_id = %q, want 'tu_r1'", got.ToolUseID)
	}

	// Should be findable by tool_use_id.
	byTU := s.GetByToolUse("tu_r1")
	if byTU == nil || byTU.ID != "art_restored1" {
		t.Error("GetByToolUse should find restored artifact")
	}
}

func TestRestoreOverwrite(t *testing.T) {
	s := NewStore()

	s.Restore(&Artifact{ID: "art_ow", Content: "v1", Size: 2})
	s.Restore(&Artifact{ID: "art_ow", Content: "v2", Size: 2})

	got := s.Get("art_ow")
	if got == nil {
		t.Fatal("artifact not found")
	}
	if got.Content != "v2" {
		t.Errorf("content = %q, want 'v2' (overwritten)", got.Content)
	}
	if s.Len() != 1 {
		t.Errorf("len = %d, want 1 (no duplicates)", s.Len())
	}
}
