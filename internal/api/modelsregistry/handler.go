// Package modelsregistry serves the model + provider catalog over HTTP.
// See docs/api/models-registry-api.md for the wire shape and semantics.
package modelsregistry

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider/registry"
)

// Handler serves /api/v1/models and /api/v1/models/{provider}/{model_id}.
type Handler struct {
	registry *registry.Registry
	logger   *zap.Logger
}

// New constructs a Handler.
func New(reg *registry.Registry, logger *zap.Logger) *Handler {
	return &Handler{registry: reg, logger: logger}
}

const pathPrefix = "/api/v1/models"

// ServeHTTP routes:
//
//	GET /api/v1/models                           → list
//	GET /api/v1/models/{provider}/{model_id}     → single
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, pathPrefix)
	switch {
	case path == "" || path == "/":
		h.list(w)
	case strings.HasPrefix(path, "/"):
		key := strings.TrimPrefix(path, "/")
		h.get(w, key)
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "expected /api/v1/models or /api/v1/models/{provider}/{model_id}")
	}
}

func (h *Handler) list(w http.ResponseWriter) {
	keys := h.registry.ListModels()
	type entry struct {
		ID string `json:"id"`
		*registry.ModelSpec
	}
	out := struct {
		Data []entry `json:"data"`
	}{Data: make([]entry, 0, len(keys))}
	for _, k := range keys {
		mod := h.registry.LookupModel(k)
		if mod == nil {
			continue
		}
		out.Data = append(out.Data, entry{ID: k, ModelSpec: mod})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) get(w http.ResponseWriter, key string) {
	mod := h.registry.LookupModel(key)
	if mod == nil {
		writeError(w, http.StatusNotFound, "model_not_found", "")
		return
	}
	type entry struct {
		ID string `json:"id"`
		*registry.ModelSpec
	}
	writeJSON(w, http.StatusOK, entry{ID: key, ModelSpec: mod})
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
