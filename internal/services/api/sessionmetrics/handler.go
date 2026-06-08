// Package sessionmetrics serves the per-session metrics dashboard
// snapshot over HTTP. See docs/superpowers/specs/2026-05-12-session-metrics-design.md.
package sessionmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/sessionstats"
	"harnessclaw-go/pkg/types"
)

// Loader is the minimal Store dependency for the handler — just the
// cold-path lookup. Production wires it to session.Store, but the
// narrow interface keeps tests simple.
type Loader interface {
	LoadSessionStats(ctx context.Context, sessionID string) (types.SessionStats, error)
}

// Handler serves GET /api/v1/sessions/{id}/metrics. Live in-memory
// trackers win over the cold SQLite snapshot so an in-flight session
// returns fresh data without waiting for the debounce flush.
type Handler struct {
	registry *sessionstats.Registry
	loader   Loader
	logger   *zap.Logger
}

// New constructs a Handler.
func New(reg *sessionstats.Registry, loader Loader, logger *zap.Logger) *Handler {
	return &Handler{registry: reg, loader: loader, logger: logger}
}

const pathPrefix = "/api/v1/sessions/"
const pathSuffix = "/metrics"

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	sessionID, ok := parseSessionID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "expected /api/v1/sessions/{id}/metrics")
		return
	}

	if tr := h.registry.Get(sessionID); tr != nil {
		writeJSON(w, http.StatusOK, tr.Snapshot())
		return
	}

	stats, err := h.loader.LoadSessionStats(r.Context(), sessionID)
	if err != nil {
		h.logger.Error("load session stats",
			zap.String("session_id", sessionID), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if stats.SessionID == "" {
		writeError(w, http.StatusNotFound, "session_not_found", "")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// parseSessionID parses /api/v1/sessions/{id}/metrics into the id.
// Empty id segment returns ok=false.
func parseSessionID(path string) (string, bool) {
	if !strings.HasPrefix(path, pathPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, pathPrefix)
	if !strings.HasSuffix(rest, pathSuffix) {
		return "", false
	}
	id := strings.TrimSuffix(rest, pathSuffix)
	if id == "" || strings.ContainsRune(id, '/') {
		return "", false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	payload := map[string]string{"error": code}
	if message != "" {
		payload["message"] = message
	}
	writeJSON(w, status, payload)
}
