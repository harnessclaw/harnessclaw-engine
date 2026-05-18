// artifacts_handler.go serves raw artifact content over HTTP so the
// Electron client (and any future REST consumer) can pull binary
// payloads without going through the LLM tool path.
//
// Why a separate endpoint instead of reusing ArtifactRead:
//
//   ArtifactRead is the LLM-facing tool — it returns a JSON envelope
//   with base64-encoded content (we keep wire shape uniform across
//   text/blob there). The client needs the *raw bytes* to hand off to
//   the OS / mammoth / pdf-parse. Doing base64-encode-then-decode round-
//   trip just to render a docx is wasteful and adds a corruption surface
//   for no benefit.
//
// Route: GET /api/v1/artifacts/{id}/content
//
//   Success: 200 with Content-Type set from artifact.MIMEType (or
//   application/octet-stream when unknown), Content-Disposition: inline,
//   Content-Length set, body = raw bytes.
//
//   Errors:
//     400 — id missing / malformed
//     404 — artifact not found / expired
//     500 — store / disk failure
//
// The endpoint deliberately does NOT do auth. The Console API in this
// codebase is a localhost-only management surface (see ServerConfig);
// hardening for remote deployment is a separate concern that should add
// auth uniformly to every route.

package api

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"harnessclaw-go/internal/artifact"
)

// ArtifactContentHandler serves raw artifact bytes. Constructed from the
// shared artifact.Store handle that the engine already uses.
type ArtifactContentHandler struct {
	store  artifact.Store
	logger *zap.Logger
}

// NewArtifactContentHandler wires the handler to a store. logger may be
// nil — uses zap.NewNop() in that case.
func NewArtifactContentHandler(store artifact.Store, logger *zap.Logger) *ArtifactContentHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ArtifactContentHandler{store: store, logger: logger.Named("artifacts_api")}
}

// RegisterRoutes mounts the handler under /api/v1/artifacts/. Called from
// server.go after the artifact store is available.
func (h *ArtifactContentHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/artifacts/", h.handle)
}

// handle dispatches GET /api/v1/artifacts/{id}/content. We do path parsing
// manually because the stdlib mux's typed segments aren't expressive
// enough to nicely require both "{id}" and the trailing "/content"
// without adding a router dep.
func (h *ArtifactContentHandler) handle(w http.ResponseWriter, r *http.Request) {
	// Path shape: /api/v1/artifacts/<id>/content
	const prefix = "/api/v1/artifacts/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == r.URL.Path { // didn't start with prefix
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "content" {
		http.Error(w, "expected /api/v1/artifacts/{id}/content", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(parts[0])
	if id == "" {
		http.Error(w, "artifact id is empty", http.StatusBadRequest)
		return
	}

	a, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.logger.Warn("artifact get failed", zap.String("id", id), zap.Error(err))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	// Resolve the bytes. Two cases:
	//   (a) BlobPath set — read from disk; this is the hybrid store path
	//       used for binary artifacts (docx / pdf / large files).
	//   (b) BlobPath empty — text artifact, Content holds it inline.
	//       If Encoding=="base64" (legacy or LLM-fed), decode; otherwise
	//       send the bytes verbatim (utf-8 strings stream fine).
	var (
		body io.ReadCloser
		size int64
	)
	if a.BlobPath != "" {
		f, ferr := os.Open(a.BlobPath)
		if ferr != nil {
			if os.IsNotExist(ferr) {
				h.logger.Warn("artifact blob missing on disk", zap.String("id", id), zap.String("path", a.BlobPath))
				http.NotFound(w, r)
				return
			}
			h.logger.Warn("open blob", zap.String("id", id), zap.Error(ferr))
			http.Error(w, "open blob failed", http.StatusInternalServerError)
			return
		}
		st, _ := f.Stat()
		if st != nil {
			size = st.Size()
		}
		body = f
	} else {
		raw := a.Content
		if a.Encoding == "base64" {
			decoded, derr := base64.StdEncoding.DecodeString(raw)
			if derr != nil {
				h.logger.Warn("base64 decode failed", zap.String("id", id), zap.Error(derr))
				http.Error(w, "decode failed", http.StatusInternalServerError)
				return
			}
			body = io.NopCloser(strings.NewReader(string(decoded)))
			size = int64(len(decoded))
		} else {
			body = io.NopCloser(strings.NewReader(raw))
			size = int64(len(raw))
		}
	}
	defer body.Close()

	// Headers. Browsers respect Content-Disposition: inline for
	// previewable types and offer "Save As" otherwise — both are fine
	// for the Electron client which writes the body to a temp file.
	mime := a.MIMEType
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if a.Name != "" {
		// Use RFC 5987 ext-value form so non-ASCII filenames (e.g. Chinese
		// docx titles) survive without being mangled by clients that
		// don't parse UTF-8 from the bare filename= parameter.
		w.Header().Set("Content-Disposition",
			"inline; filename*=UTF-8''"+urlEscape(a.Name))
	}
	if a.Checksum != "" {
		w.Header().Set("ETag", a.Checksum)
	}

	// Stream the bytes. Per io.Copy contract, errors here are typically
	// "client went away" — we log at debug to avoid noise.
	if _, err := io.Copy(w, body); err != nil {
		h.logger.Debug("stream copy error",
			zap.String("id", id), zap.Error(err))
	}
}

// urlEscape is a minimal RFC 3986 percent-encoder for the bytes we
// commonly see in artifact names — mostly Chinese characters (which
// must be encoded for header values). Imports url.PathEscape from the
// stdlib would do too, but this avoids the extra import for a 6-line
// loop.
func urlEscape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}
