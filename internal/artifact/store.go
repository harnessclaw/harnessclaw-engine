package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when the requested artifact does not exist (or
// has expired and been garbage-collected). Tools translate this into a
// human-readable error so the LLM can recover without crashing the loop.
var ErrNotFound = errors.New("artifact: not found")

// ErrAccessDenied is returned when the caller's scope cannot read the
// artifact (e.g. trying to read a trace-scoped artifact from a different
// trace). Listed separately from ErrNotFound to avoid leaking existence
// information across scope boundaries.
var ErrAccessDenied = errors.New("artifact: access denied")

// ReadMode controls how much of an artifact Read returns. Doc §5 — the
// LLM is expected to scan metadata first, peek at preview, then fetch
// full content only when it knows it needs the bytes.
type ReadMode string

const (
	// ModeMetadata returns name/description/preview/size; no Content.
	ModeMetadata ReadMode = "metadata"
	// ModePreview returns metadata + the truncated preview only.
	ModePreview ReadMode = "preview"
	// ModeFull returns metadata + the entire Content payload.
	ModeFull ReadMode = "full"
)

// IsValidMode is a small helper; tools use it to validate input from the
// LLM before hitting the store.
func IsValidMode(m ReadMode) bool {
	return m == ModeMetadata || m == ModePreview || m == ModeFull
}

// SaveInput is what producers hand to Store.Save. The store fills in the
// derived fields (ID, CreatedAt, ExpiresAt, Size, Checksum, Preview, Version)
// — producers only specify intent.
type SaveInput struct {
	Type        Type
	MIMEType    string
	Encoding    string
	Name        string
	Description string
	Content     string
	Schema      json.RawMessage
	Tags        []string

	Producer  Producer
	TraceID   string
	SessionID string

	// ParentArtifactID, when non-empty, marks this Save as a new version
	// of an existing artifact. The store reads parent.Version and writes
	// parent.Version+1.
	ParentArtifactID string

	// Access defaults to {Scope: ScopeTrace, ReadableBy: ["*"]} when zero.
	Access Access

	// TTL overrides the store's default. Zero means use store default.
	TTL time.Duration
}

// ListFilter narrows artifact.List output. Empty filter means "all
// artifacts visible in scope" — backends apply scope filtering first.
type ListFilter struct {
	TraceID   string
	SessionID string
	AgentID   string
	Tag       string
}

// Store is the storage-agnostic interface tools and the engine talk to.
// Implementations live alongside their backing tech (memory.go / sqlite).
//
// Concurrency: every method must be safe for concurrent calls from many
// goroutines. The artifact records returned by Get/List are safe to
// mutate locally — callers never share them.
type Store interface {
	// Save persists a new artifact and returns the stored record (with
	// ID, CreatedAt, etc. populated). Doc §8 — Save always produces a
	// new ID; "modifying" an artifact means producing a new version.
	Save(ctx context.Context, in *SaveInput) (*Artifact, error)

	// Get returns the full artifact by ID. ErrNotFound when absent or
	// expired.
	Get(ctx context.Context, id string) (*Artifact, error)

	// List returns artifacts matching filter, sorted by CreatedAt desc.
	List(ctx context.Context, filter *ListFilter) ([]*Artifact, error)

	// Delete removes an artifact. Used by the TTL janitor and by
	// session/trace cleanup; tools never call this directly.
	Delete(ctx context.Context, id string) error

	// PurgeExpired deletes all artifacts whose ExpiresAt is before `now`.
	// Returns the count purged. Called periodically by the janitor.
	PurgeExpired(ctx context.Context, now time.Time) (int, error)

	// Close releases any underlying resources (DB handles, etc.).
	Close() error
}

// Config holds tunables shared by every backend.
type Config struct {
	// DefaultTTL is applied when SaveInput.TTL is zero. Doc §9 recommends
	// 1h for intermediate artifacts; production may bump session/user
	// scoped artifacts via per-call overrides.
	DefaultTTL time.Duration

	// PreviewBytes overrides the default preview size cap.
	PreviewBytes int
}

// DefaultConfig returns the production-safe defaults.
func DefaultConfig() Config {
	return Config{
		DefaultTTL:   1 * time.Hour,
		PreviewBytes: DefaultPreviewBytes,
	}
}

// resolveSaveInput fills in defaults / derived fields. Shared by all
// backends so the on-disk shape is identical regardless of where the
// artifact landed.
func resolveSaveInput(in *SaveInput, cfg Config, parent *Artifact, now time.Time) *Artifact {
	previewBytes := cfg.PreviewBytes
	if previewBytes <= 0 {
		previewBytes = DefaultPreviewBytes
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = cfg.DefaultTTL
	}

	access := in.Access
	if access.Scope == "" {
		access.Scope = ScopeTrace
	}
	if access.ReadableBy == nil {
		access.ReadableBy = []string{"*"}
	}

	version := 1
	parentID := in.ParentArtifactID
	if parent != nil {
		version = parent.Version + 1
		// Derive the trace/session from parent if caller didn't say,
		// keeping the version chain inside one scope.
		if in.TraceID == "" {
			in.TraceID = parent.TraceID
		}
		if in.SessionID == "" {
			in.SessionID = parent.SessionID
		}
	}

	a := &Artifact{
		ID:               NewID(),
		TraceID:          in.TraceID,
		SessionID:        in.SessionID,
		Type:             in.Type,
		MIMEType:         in.MIMEType,
		Encoding:         in.Encoding,
		Name:             in.Name,
		Description:      in.Description,
		Size:             len(in.Content),
		Checksum:         Checksum(in.Content),
		Preview:          MakePreview(in.Content, previewBytes),
		Schema:           in.Schema,
		Producer:         in.Producer,
		CreatedAt:        now,
		Version:          version,
		ParentArtifactID: parentID,
		Access:           access,
		Tags:             append([]string(nil), in.Tags...),
		Content:          in.Content,
	}
	if ttl > 0 {
		a.ExpiresAt = now.Add(ttl)
	}
	if a.URI == "" {
		a.URI = "artifact://" + a.ID
	}
	return a
}

// canRead checks whether requester is allowed by the artifact's Access.
// MVP rule: "*" allows everyone in scope; otherwise the requester's
// AgentID must match an entry in ReadableBy. Scope itself is enforced by
// the caller via ListFilter / context (we don't carry session_id into
// every Get to keep the interface narrow).
func canRead(a *Artifact, agentID string) bool {
	if len(a.Access.ReadableBy) == 0 {
		// Empty list means "only producer".
		return agentID != "" && agentID == a.Producer.AgentID
	}
	for _, allowed := range a.Access.ReadableBy {
		if allowed == "*" || allowed == agentID {
			return true
		}
	}
	return false
}
