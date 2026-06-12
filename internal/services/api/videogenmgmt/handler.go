package videogenmgmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/config/persist"
	videogen "harnessclaw-go/internal/tools/builtin/videogen"
)

// Handler serves GET/PATCH /api/v1/videogen. It owns the live video config via
// the videogen.Source and persists mutations to cfgPath (dual-write, like
// providersmgmt).
type Handler struct {
	src     *videogen.Source
	cfgPath string
	logger  *zap.Logger
}

func New(src *videogen.Source, cfgPath string, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{src: src, cfgPath: cfgPath, logger: logger.Named("videogenmgmt")}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.get(w, r)
	case http.MethodPatch:
		h.patch(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
	}
}

func (h *Handler) get(w http.ResponseWriter, _ *http.Request) {
	writeSuccess(w, http.StatusOK, map[string]any{
		"config_source": h.cfgPath,
		"providers":     h.src.Snapshot().Providers,
	})
}

type patchRequest struct {
	Providers map[string]patchProvider `json:"providers"`
}

type patchProvider struct {
	APIKey    *string                  `json:"api_key,omitempty"`
	BaseURL   *string                  `json:"base_url,omitempty"`
	Endpoints map[string]patchEndpoint `json:"endpoints,omitempty"`
}

type patchEndpoint struct {
	Model string `json:"model"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Providers) == 0 {
		writeError(w, http.StatusBadRequest, "empty_patch", "patch must include at least one provider")
		return
	}

	// Merge onto the current snapshot so partial patches (e.g. only api_key) work.
	next := h.src.Snapshot()
	if next.Providers == nil {
		next.Providers = map[string]config.VideoProviderConfig{}
	}
	for name, pp := range req.Providers {
		cur := next.Providers[name] // zero value if new
		if cur.Endpoints == nil {
			cur.Endpoints = map[string]config.VideoEndpointConfig{}
		}
		if pp.APIKey != nil {
			cur.APIKey = *pp.APIKey
		}
		if pp.BaseURL != nil {
			cur.BaseURL = *pp.BaseURL
		}
		// Endpoints, when provided, replace the whole map (client sends the full set).
		if pp.Endpoints != nil {
			eps := make(map[string]config.VideoEndpointConfig, len(pp.Endpoints))
			for epName, ep := range pp.Endpoints {
				eps[epName] = config.VideoEndpointConfig{Model: ep.Model}
			}
			cur.Endpoints = eps
		}
		next.Providers[name] = cur
	}

	// Apply to the live Source first (source of truth for running tools).
	h.src.UpdateProviders(next)

	// Persist (dual-write). On failure, in-memory state still reflects the change.
	if err := h.persist(next); err != nil {
		h.logger.Error("persist videogen failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "persist_failed", err.Error())
		return
	}

	writeSuccess(w, http.StatusOK, map[string]any{
		"config_source": h.cfgPath,
		"providers":     next.Providers,
	})
}

func (h *Handler) persist(cfg config.VideoGenConfig) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	f, err := persist.Load(h.cfgPath)
	if err != nil {
		return err
	}
	if err := f.SetVideoGen(cfg); err != nil {
		return err
	}
	return f.Save()
}

// --- response envelope (identical to providersmgmt) ---

type apiResponse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeSuccess(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, apiResponse{Code: "OK", Data: data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiResponse{Code: code, Message: message})
}
