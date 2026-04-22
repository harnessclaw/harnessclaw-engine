// Package artifact provides a session-scoped content store for large tool results.
//
// When a tool produces output exceeding a configurable threshold, the full
// content is persisted in the Store and the session message receives a
// compact preview instead of the full text. This avoids sending the same
// large payload to the LLM on every subsequent turn, significantly reducing
// input token usage.
package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// DefaultThreshold is the minimum content length (in bytes) for a tool result
// to be persisted as an artifact. Results shorter than this are kept inline.
const DefaultThreshold = 4096

// DefaultPreviewLen is the number of leading characters included in the
// preview that replaces the full content in the session message.
const DefaultPreviewLen = 2048

// Artifact holds a single persisted tool result.
type Artifact struct {
	ID        string         `json:"id"`
	ToolUseID string         `json:"tool_use_id"`
	ToolName  string         `json:"tool_name"`
	Content   string         `json:"content"`
	Summary   string         `json:"summary"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Size      int            `json:"size"`
	CreatedAt time.Time      `json:"created_at"`
}

// Store is a concurrency-safe, session-scoped artifact store.
type Store struct {
	mu        sync.RWMutex
	artifacts map[string]*Artifact // id → artifact
	byToolUse map[string]string    // tool_use_id → artifact id
}

// NewStore creates an empty artifact store.
func NewStore() *Store {
	return &Store{
		artifacts: make(map[string]*Artifact),
		byToolUse: make(map[string]string),
	}
}

// Save persists content as an artifact and returns the generated ID.
func (s *Store) Save(toolUseID, toolName, content string, meta map[string]any) string {
	id := generateID()
	now := time.Now()

	art := &Artifact{
		ID:        id,
		ToolUseID: toolUseID,
		ToolName:  toolName,
		Content:   content,
		Summary:   truncate(content, 200),
		Metadata:  meta,
		Size:      len(content),
		CreatedAt: now,
	}

	s.mu.Lock()
	s.artifacts[id] = art
	if toolUseID != "" {
		s.byToolUse[toolUseID] = id
	}
	s.mu.Unlock()

	return id
}

// Get returns an artifact by ID, or nil if not found.
func (s *Store) Get(id string) *Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.artifacts[id]
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// GetByToolUse returns an artifact by the tool_use_id that produced it.
func (s *Store) GetByToolUse(toolUseID string) *Artifact {
	s.mu.RLock()
	id, ok := s.byToolUse[toolUseID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.Get(id)
}

// Ref returns a compact reference string suitable for inclusion in a message
// sent to the LLM. It contains a short summary and instructions for retrieval.
func (s *Store) Ref(id string) string {
	s.mu.RLock()
	a := s.artifacts[id]
	s.mu.RUnlock()
	if a == nil {
		return ""
	}
	return fmt.Sprintf("[Artifact %s: %s (%d chars)] Full content available via artifact_id=%s",
		a.ID, a.Summary, a.Size, a.ID)
}

// Preview builds an inline preview for a newly persisted artifact. The preview
// includes the leading portion of the content plus a footer noting the artifact ID.
func (s *Store) Preview(id string, maxLen int) string {
	s.mu.RLock()
	a := s.artifacts[id]
	s.mu.RUnlock()
	if a == nil {
		return ""
	}
	if maxLen <= 0 {
		maxLen = DefaultPreviewLen
	}
	preview := truncate(a.Content, maxLen)
	if len(a.Content) > maxLen {
		preview += fmt.Sprintf("\n\n... [truncated, full content persisted as artifact %s (%d chars). Use ArtifactGet to retrieve.]",
			a.ID, a.Size)
	}
	return preview
}

// List returns a shallow copy of all stored artifacts, ordered by creation time.
func (s *Store) List() []*Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Artifact, 0, len(s.artifacts))
	for _, a := range s.artifacts {
		cp := *a
		result = append(result, &cp)
	}
	return result
}

// Len returns the number of stored artifacts.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.artifacts)
}

// TotalSize returns the sum of all artifact content sizes in bytes.
func (s *Store) TotalSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, a := range s.artifacts {
		total += a.Size
	}
	return total
}

// Restore inserts an artifact with its original ID, preserving all fields.
// This is used when loading artifacts from persistent storage (e.g. SQLite).
// If an artifact with the same ID already exists, it is overwritten.
func (s *Store) Restore(art *Artifact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts[art.ID] = art
	if art.ToolUseID != "" {
		s.byToolUse[art.ToolUseID] = art.ID
	}
}

// generateID returns a unique artifact ID of the form "art_" + 8 hex chars.
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails.
		return fmt.Sprintf("art_%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return "art_" + hex.EncodeToString(b)
}

// truncate returns the first n characters of s (rune-safe).
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
