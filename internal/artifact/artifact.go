// Package artifact implements the shared artifact store described in
// docs/protocols/artifacts.md (chapters 1–11).
//
// An artifact is an addressable, immutable, structured product of an agent's
// work. Agents pass artifact IDs (not content) to each other so large data
// stays out of LLM context while remaining traceable, schema-validated, and
// permission-scoped.
package artifact

import (
	"encoding/json"
	"time"
)

// Type classifies what an artifact actually contains. The store treats all
// types uniformly; the field exists so consumers know whether to call Read
// with mode=full (small structured), mode=preview (large file), or hand the
// URI to a code-execution tool (binary blob).
type Type string

const (
	// TypeStructured is JSON-shaped data with a Schema. Safe to read full.
	TypeStructured Type = "structured"
	// TypeFile is text content (markdown, csv, source). Use mode=preview first.
	TypeFile Type = "file"
	// TypeBlob is binary content (image, pdf, archive). Reading full into the
	// LLM is almost always wrong; route to code execution instead.
	TypeBlob Type = "blob"
)

// Scope controls who can read an artifact. An artifact is always created
// inside one trace; Scope decides whether it remains visible only there
// (default), survives across traces in the same session, etc.
type Scope string

const (
	// ScopeTrace limits visibility to producers and consumers in the same trace_id.
	// Default — the safest choice; matches "intermediate" TTL semantics.
	ScopeTrace Scope = "trace"
	// ScopeSession allows reuse across traces of the same session (e.g. the
	// user's previous research output is still readable in their next turn).
	ScopeSession Scope = "session"
	// ScopeUser allows reuse across sessions of the same user. Only set when
	// the user has explicitly pinned the artifact for keeping.
	ScopeUser Scope = "user"
)

// Producer records who created the artifact. Required on every Save —
// without producer/consumers the store has no lineage and debugging an
// "incorrect conclusion" trace becomes impossible.
type Producer struct {
	AgentID    string `json:"agent_id"`
	AgentRunID string `json:"agent_run_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
}

// Access is the per-artifact read/write ACL. ReadableBy / WritableBy take a
// list of agent role/IDs or "*" for "anyone in scope". An empty list means
// "only the producer", which is the strictest default.
type Access struct {
	Scope       Scope    `json:"scope"`
	ReadableBy  []string `json:"readable_by,omitempty"`
	WritableBy  []string `json:"writable_by,omitempty"`
}

// Artifact is the full record. Fields map 1:1 to the doc §4 schema. The
// Content field is held inline by in-memory backends; SQLite/object-store
// backends may keep Content empty in metadata listings and load it on
// demand via the URI.
type Artifact struct {
	ID        string `json:"artifact_id"`
	TraceID   string `json:"trace_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	Type     Type   `json:"type"`
	MIMEType string `json:"mime_type,omitempty"`
	Encoding string `json:"encoding,omitempty"`

	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Size        int    `json:"size_bytes"`
	Checksum    string `json:"checksum,omitempty"`

	URI     string `json:"uri,omitempty"`
	Preview string `json:"preview,omitempty"`

	// Schema is opaque JSON describing structured payloads (e.g. table
	// columns). Stored as RawMessage so producers can put whatever they
	// need without coupling the store to one schema dialect.
	Schema json.RawMessage `json:"schema,omitempty"`

	Producer  Producer `json:"producer"`
	Consumers []string `json:"consumers,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	Version          int    `json:"version"`
	ParentArtifactID string `json:"parent_artifact_id,omitempty"`

	Access Access   `json:"access"`
	Tags   []string `json:"tags,omitempty"`

	// Content is the inline payload. Required on Save; backends may strip
	// it when returning metadata-only views.
	Content string `json:"content,omitempty"`
}

// Ref is the lightweight handle agents pass to each other. Carries enough
// for the LLM to decide whether to read full content, without paying the
// token cost of the full Artifact.
type Ref struct {
	ID          string `json:"artifact_id"`
	Name        string `json:"name,omitempty"`
	Type        Type   `json:"type"`
	MIMEType    string `json:"mime_type,omitempty"`
	Size        int    `json:"size_bytes"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// ToRef returns a lightweight Ref for embedding in tool_result / events.
func (a *Artifact) ToRef() Ref {
	return Ref{
		ID:          a.ID,
		Name:        a.Name,
		Type:        a.Type,
		MIMEType:    a.MIMEType,
		Size:        a.Size,
		Description: a.Description,
		Preview:     a.Preview,
	}
}

// IsExpired returns true if the artifact's TTL has elapsed. Zero ExpiresAt
// means "no expiry" (e.g. user-pinned persistent artifacts).
func (a *Artifact) IsExpired(now time.Time) bool {
	if a.ExpiresAt.IsZero() {
		return false
	}
	return now.After(a.ExpiresAt)
}
