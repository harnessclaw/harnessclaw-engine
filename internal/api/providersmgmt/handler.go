// Package providersmgmt serves the runtime provider-management API:
//
//	GET    /api/v1/providers                      list providers (api_key masked)
//	GET    /api/v1/providers/fallback-chain       current chain + per-entry health
//	PATCH  /api/v1/providers/{name}               update model/api_key/base_url
//	PUT    /api/v1/providers/fallback-chain       replace chain ordering
//
// Mutations are dual-write: the in-memory manager applies them first
// (source of truth for the live engine), and the yaml config file is
// rewritten on success. If yaml persistence fails the call returns
// 5xx — the in-memory state still reflects the new value, so the
// running server stays on the new config but a restart would revert
// to the old yaml. Operators see this in the response and can retry
// or fix the disk issue.
package providersmgmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/config/persist"
	"harnessclaw-go/internal/provider/manager"
)

// Handler implements http.Handler for the providers management API.
type Handler struct {
	mgr     *manager.Manager
	cfgPath string
	logger  *zap.Logger
}

// New constructs a Handler. cfgPath is the on-disk yaml that every
// mutator rewrites — pass the same path the server loaded from at
// startup.
func New(mgr *manager.Manager, cfgPath string, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{mgr: mgr, cfgPath: cfgPath, logger: logger}
}

const pathPrefix = "/api/v1/providers"

// ServeHTTP dispatches to per-endpoint handlers.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, pathPrefix)

	switch {
	case path == "" || path == "/":
		if r.Method == http.MethodGet {
			h.listProviders(w)
			return
		}
	case path == "/fallback-chain":
		switch r.Method {
		case http.MethodGet:
			h.getChain(w)
			return
		case http.MethodPut:
			h.replaceChain(w, r)
			return
		}
	case strings.HasPrefix(path, "/"):
		name := strings.TrimPrefix(path, "/")
		if name == "" || strings.Contains(name, "/") {
			writeError(w, http.StatusBadRequest, "bad_request", "expected /api/v1/providers/{name}")
			return
		}
		if r.Method == http.MethodPatch {
			h.patchProvider(w, name, r)
			return
		}
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
		fmt.Sprintf("method %s not supported on %s", r.Method, r.URL.Path))
}

// ---------- handlers ----------

func (h *Handler) listProviders(w http.ResponseWriter) {
	writeSuccess(w, http.StatusOK, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
	})
}

func (h *Handler) getChain(w http.ResponseWriter) {
	writeSuccess(w, http.StatusOK, h.mgr.ChainSnapshot())
}

// PatchProviderRequest is the JSON body for PATCH /providers/{name}.
// All fields are optional; nil means "leave unchanged".
type PatchProviderRequest struct {
	Model   *string `json:"model,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
}

func (h *Handler) patchProvider(w http.ResponseWriter, name string, r *http.Request) {
	var body PatchProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	patch := manager.ProviderPatch{
		Model:   body.Model,
		APIKey:  body.APIKey,
		BaseURL: body.BaseURL,
	}
	if patch.IsEmpty() {
		writeError(w, http.StatusBadRequest, "bad_request", "no fields to update; supply at least one of model/api_key/base_url")
		return
	}
	if err := h.mgr.UpdateProvider(name, patch); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}

	// Persist to yaml. On failure the in-memory state is already
	// updated; warn the caller so they can retry / fix disk.
	if err := h.persistProvider(name); err != nil {
		h.logger.Warn("providersmgmt: yaml persist failed after in-memory patch",
			zap.String("provider", name),
			zap.Error(err),
		)
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: provider patched",
		zap.String("name", name),
		zap.Bool("model_changed", body.Model != nil),
		zap.Bool("api_key_changed", body.APIKey != nil),
		zap.Bool("base_url_changed", body.BaseURL != nil),
	)
	// Return the fresh snapshot (with mask) so the client can re-render.
	writeSuccess(w, http.StatusOK, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
	})
}

// ReplaceChainRequest is the JSON body for PUT /fallback-chain.
type ReplaceChainRequest struct {
	Chain []string `json:"chain"`
}

func (h *Handler) replaceChain(w http.ResponseWriter, r *http.Request) {
	var body ReplaceChainRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if len(body.Chain) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "chain must be non-empty")
		return
	}
	if err := h.mgr.ReplaceChain(body.Chain); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistChain(body.Chain); err != nil {
		h.logger.Warn("providersmgmt: yaml persist failed after in-memory chain replace",
			zap.Strings("chain", body.Chain),
			zap.Error(err),
		)
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: chain replaced", zap.Strings("chain", body.Chain))
	writeSuccess(w, http.StatusOK, h.mgr.ChainSnapshot())
}

// ---------- persistence helpers ----------

// persistProvider rewrites llm.providers[name] in the on-disk yaml.
// Pulls the current ProviderConfig from the in-memory manager
// (post-patch) so the yaml matches the live state byte-for-byte.
func (h *Handler) persistProvider(name string) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	llmCfg := h.mgr.CurrentConfig()
	provCfg, ok := llmCfg.Providers[name]
	if !ok {
		return fmt.Errorf("provider %q vanished from in-memory state", name)
	}
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		return f.SetProvider(name, provCfg)
	})
}

// persistChain rewrites llm.fallback_chain in the on-disk yaml.
func (h *Handler) persistChain(chain []string) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		return f.SetFallbackChain(chain)
	})
}

// mutateYAML loads → mutates → saves the yaml file. Wraps the
// standard load-modify-save pattern so every mutator goes through
// one place.
func mutateYAML(path string, fn func(f *persist.File) error) error {
	f, err := persist.Load(path)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return f.Save()
}

// ---------- response helpers (local copies to avoid importing api) ----------

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

// (Linter-quieting) keep config import used; future endpoints may
// want to surface health params directly.
var _ = config.LLMConfig{}
