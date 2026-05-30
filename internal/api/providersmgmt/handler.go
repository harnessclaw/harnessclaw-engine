// Package providersmgmt serves the runtime provider-management API.
//
// Data model:
//
//	providers (credentials)
//	  └── endpoints (chain-routable model bindings)
//
//	agent.primary         (dotted "provider:endpoint" — main model)
//	agent.fallback_chain  ([]string of dotted refs — backups)
//
// Routes:
//
//	GET    /api/v1/providers
//	POST   /api/v1/providers
//	PATCH  /api/v1/providers/{p}
//	GET    /api/v1/providers/{p}/endpoints
//	POST   /api/v1/providers/{p}/endpoints
//	PATCH  /api/v1/providers/{p}/endpoints/{e}
//	DELETE /api/v1/providers/{p}/endpoints/{e}
//	GET    /api/v1/agent
//	PATCH  /api/v1/agent
//
// Mutations are dual-write: the in-memory manager applies them first
// (source of truth for the live engine), and the yaml config file is
// rewritten on success. If yaml persistence fails the call returns
// 5xx — the in-memory state still reflects the new value.
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
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/provider/manager"
	modelregistry "harnessclaw-go/internal/provider/registry"
)

// Handler implements http.Handler for the providers management API.
type Handler struct {
	mgr     *manager.Manager
	cfgPath string
	logger  *zap.Logger
}

// New constructs a Handler. cfgPath is the on-disk yaml that every
// mutator rewrites.
func New(mgr *manager.Manager, cfgPath string, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{mgr: mgr, cfgPath: cfgPath, logger: logger}
}

// ServeHTTP dispatches on path + method. Designed to be mounted at
// two roots: "/api/v1/providers" / "/api/v1/providers/" and
// "/api/v1/agent" / "/api/v1/agent/".
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/v1/providers" || path == "/api/v1/providers/":
		switch r.Method {
		case http.MethodGet:
			h.listProviders(w)
			return
		case http.MethodPost:
			h.addProvider(w, r)
			return
		}
	case strings.HasPrefix(path, "/api/v1/providers/"):
		rest := strings.TrimPrefix(path, "/api/v1/providers/")
		parts := strings.Split(rest, "/")
		switch len(parts) {
		case 1: // /providers/{p}
			h.routeProvider(w, r, parts[0])
			return
		case 2: // /providers/{p}/endpoints
			if parts[1] == "endpoints" || parts[1] == "endpoints/" {
				h.routeEndpointCollection(w, r, parts[0])
				return
			}
		case 3: // /providers/{p}/endpoints/{e}
			if parts[1] == "endpoints" && parts[2] != "" {
				h.routeEndpoint(w, r, parts[0], parts[2])
				return
			}
		}
	case path == "/api/v1/agent" || path == "/api/v1/agent/":
		h.routeAgent(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found",
		fmt.Sprintf("no route for %s %s", r.Method, path))
}

// ---------- /providers ----------

// AddProviderRequest is the body for POST /providers.
// name + type are required; base_url and api_key default to empty
// (empty api_key means LLM calls will fail until PATCHed in).
type AddProviderRequest struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Disabled bool   `json:"disabled"`
}

func (h *Handler) addProvider(w http.ResponseWriter, r *http.Request) {
	var body AddProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" || body.Type == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"name and type are required")
		return
	}
	if _, ok := bifrost.ProviderTypeOf(body.Type); !ok {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("type %q not allowed; expected one of %v", body.Type, bifrost.AllowedTypeNames()))
		return
	}
	pc := config.ProviderConfig{
		Type:     body.Type,
		BaseURL:  body.BaseURL,
		APIKey:   body.APIKey,
		Disabled: body.Disabled,
	}
	if err := h.mgr.AddProvider(body.Name, pc); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistProviderCreds(body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: provider added",
		zap.String("name", body.Name),
		zap.String("type", body.Type),
	)
	writeSuccess(w, http.StatusCreated, map[string]any{
		"config_source": h.cfgPath,
		"providers":     h.mgr.ProvidersSnapshot(),
	})
}

func (h *Handler) listProviders(w http.ResponseWriter) {
	writeSuccess(w, http.StatusOK, map[string]any{
		// config_source surfaces the absolute path of the yaml that
		// mutations write back to — i.e. the file viper actually
		// loaded at startup. Lets clients confirm which config file
		// they're editing without grepping the server log.
		"config_source": h.cfgPath,
		"providers":     h.mgr.ProvidersSnapshot(),
	})
}

func (h *Handler) routeProvider(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "provider name required")
		return
	}
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"only PATCH supported on /providers/{name}; use POST /api/v1/providers to create a new provider")
		return
	}
	h.patchProviderCreds(w, r, name)
}

// PatchProviderRequest is the body for PATCH /providers/{name}.
type PatchProviderRequest struct {
	Type     *string `json:"type,omitempty"`
	BaseURL  *string `json:"base_url,omitempty"`
	APIKey   *string `json:"api_key,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

func (h *Handler) patchProviderCreds(w http.ResponseWriter, r *http.Request, name string) {
	var body PatchProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if body.Type != nil {
		if _, ok := bifrost.ProviderTypeOf(*body.Type); !ok {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("type %q not allowed; expected one of %v", *body.Type, bifrost.AllowedTypeNames()))
			return
		}
	}
	patch := manager.ProviderCredsPatch{
		Type:     body.Type,
		BaseURL:  body.BaseURL,
		APIKey:   body.APIKey,
		Disabled: body.Disabled,
	}
	if patch.IsEmpty() {
		writeError(w, http.StatusBadRequest, "bad_request",
			"empty patch; supply at least one of type/base_url/api_key/disabled")
		return
	}
	if err := h.mgr.UpdateProviderCreds(name, patch); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistProviderCreds(name); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: provider creds patched",
		zap.String("provider", name),
		zap.Bool("type_changed", body.Type != nil),
		zap.Bool("base_url_changed", body.BaseURL != nil),
		zap.Bool("api_key_changed", body.APIKey != nil),
		zap.Bool("disabled_changed", body.Disabled != nil),
	)
	writeSuccess(w, http.StatusOK, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
		"agent":     h.mgr.AgentSnapshot(),
	})
}

// ---------- /providers/{p}/endpoints ----------

func (h *Handler) routeEndpointCollection(w http.ResponseWriter, r *http.Request, provName string) {
	switch r.Method {
	case http.MethodGet:
		h.listEndpoints(w, provName)
	case http.MethodPost:
		h.addEndpoint(w, r, provName)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("method %s not supported on /providers/%s/endpoints", r.Method, provName))
	}
}

func (h *Handler) listEndpoints(w http.ResponseWriter, provName string) {
	for _, p := range h.mgr.ProvidersSnapshot() {
		if p.Name == provName {
			writeSuccess(w, http.StatusOK, map[string]any{
				"endpoints": p.Endpoints,
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found",
		fmt.Sprintf("provider %q not found", provName))
}

// AddEndpointRequest is the body for POST /providers/{p}/endpoints.
// name + model are required; others use system defaults.
type AddEndpointRequest struct {
	Name           string   `json:"name"`
	Model          string   `json:"model"`
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	EnableThinking *bool    `json:"enable_thinking,omitempty"`
	Disabled       *bool    `json:"disabled,omitempty"`
	Group          string   `json:"group,omitempty"`
}

func (h *Handler) addEndpoint(w http.ResponseWriter, r *http.Request, provName string) {
	var body AddEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" || body.Model == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"name and model are required")
		return
	}
	// Endpoint names may contain '.' (e.g. "gpt-5.5",
	// "claude-3.5-sonnet"). Only ':' is forbidden — it's the
	// canonical chain ref separator and would create ambiguity.
	if strings.Contains(body.Name, ":") {
		writeError(w, http.StatusBadRequest, "bad_request",
			"endpoint name cannot contain ':' (canonical chain ref separator)")
		return
	}
	ep := config.EndpointConfig{Model: body.Model}
	if body.MaxTokens != nil {
		ep.MaxTokens = *body.MaxTokens
	}
	if body.Temperature != nil {
		ep.Temperature = *body.Temperature
	}
	if body.EnableThinking != nil {
		v := *body.EnableThinking
		ep.EnableThinking = &v
	}
	if body.Disabled != nil {
		ep.Disabled = *body.Disabled
	}
	if body.Group != "" {
		ep.Group = body.Group
	}
	if err := h.mgr.AddEndpoint(provName, body.Name, ep); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistEndpoint(provName, body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: endpoint added",
		zap.String("provider", provName),
		zap.String("endpoint", body.Name),
		zap.String("model", body.Model),
	)
	writeSuccess(w, http.StatusCreated, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
	})
}

// ---------- /providers/{p}/endpoints/{e} ----------

func (h *Handler) routeEndpoint(w http.ResponseWriter, r *http.Request, provName, epName string) {
	switch r.Method {
	case http.MethodPatch:
		h.patchEndpoint(w, r, provName, epName)
	case http.MethodDelete:
		h.deleteEndpoint(w, provName, epName)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("method %s not supported on endpoint resource", r.Method))
	}
}

// PatchEndpointRequest is the body for PATCH /providers/{p}/endpoints/{e}.
//
// ModelType uses *[]string so callers can distinguish "field omitted —
// leave alone" (nil) from "set to []  — explicitly clear the override
// and revert to manifest baseline" (non-nil pointer to empty slice).
type PatchEndpointRequest struct {
	Model          *string   `json:"model,omitempty"`
	MaxTokens      *int      `json:"max_tokens,omitempty"`
	Temperature    *float64  `json:"temperature,omitempty"`
	EnableThinking *bool     `json:"enable_thinking,omitempty"`
	Disabled       *bool     `json:"disabled,omitempty"`
	ModelType      *[]string `json:"model_type,omitempty"`
}

func (h *Handler) patchEndpoint(w http.ResponseWriter, r *http.Request, provName, epName string) {
	var body PatchEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var filteredModelType *[]string
	if body.ModelType != nil {
		known, unknown := modelregistry.FilterKnownTokens(*body.ModelType)
		if len(unknown) > 0 {
			writeError(w, http.StatusBadRequest, "invalid_model_type",
				fmt.Sprintf("unknown tokens: %v; allowed: vision/pdf/audio/video/reasoning/tools/search", unknown))
			return
		}
		// known may be nil when caller sent []  — preserve that distinction
		// (nil-but-non-nil-pointer means "clear override").
		if known == nil {
			known = []string{}
		}
		filteredModelType = &known
	}
	patch := manager.EndpointPatch{
		Model:          body.Model,
		MaxTokens:      body.MaxTokens,
		Temperature:    body.Temperature,
		EnableThinking: body.EnableThinking,
		Disabled:       body.Disabled,
		ModelType:      filteredModelType,
	}
	if patch.IsEmpty() {
		writeError(w, http.StatusBadRequest, "bad_request",
			"empty patch; supply at least one of model/max_tokens/temperature/enable_thinking/disabled/model_type")
		return
	}
	if err := h.mgr.UpdateEndpoint(provName, epName, patch); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistEndpoint(provName, epName); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: endpoint patched",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
	)
	writeSuccess(w, http.StatusOK, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
		"agent":     h.mgr.AgentSnapshot(),
	})
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, provName, epName string) {
	if err := h.mgr.DeleteEndpoint(provName, epName); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistEndpointDeletion(provName, epName); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: endpoint deleted",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
	)
	writeSuccess(w, http.StatusOK, map[string]any{
		"providers": h.mgr.ProvidersSnapshot(),
		"agent":     h.mgr.AgentSnapshot(),
	})
}

// ---------- /agent ----------

func (h *Handler) routeAgent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeSuccess(w, http.StatusOK, h.mgr.AgentSnapshot())
	case http.MethodPatch:
		h.patchAgent(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("method %s not supported on /agent", r.Method))
	}
}

// PatchAgentRequest is the body for PATCH /agent. All fields are
// optional; only fields present in the JSON are mutated.
// FallbackChain accepts an empty array to clear the chain (distinct
// from omitting the key, which leaves it unchanged).
type PatchAgentRequest struct {
	Primary           *string   `json:"primary,omitempty"`
	FallbackChain     *[]string `json:"fallback_chain,omitempty"`
	MaxTokens         *int      `json:"max_tokens,omitempty"`
	Temperature       *float64  `json:"temperature,omitempty"`
	ContextWindow     *int      `json:"context_window,omitempty"`
	MaxTurns          *int      `json:"max_turns,omitempty"`
	MaxToolCalls      *int      `json:"max_tool_calls,omitempty"`
	ThinkingIntensity *string   `json:"thinking_intensity,omitempty"`
}

func (h *Handler) patchAgent(w http.ResponseWriter, r *http.Request) {
	var body PatchAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	patch := manager.AgentPatch{
		Primary:           body.Primary,
		FallbackChain:     body.FallbackChain,
		MaxTokens:         body.MaxTokens,
		Temperature:       body.Temperature,
		ContextWindow:     body.ContextWindow,
		MaxTurns:          body.MaxTurns,
		MaxToolCalls:      body.MaxToolCalls,
		ThinkingIntensity: body.ThinkingIntensity,
	}
	if patch.IsEmpty() {
		writeError(w, http.StatusBadRequest, "bad_request",
			"empty patch; supply at least one of primary/fallback_chain/max_tokens/temperature/context_window/max_turns/max_tool_calls/thinking_intensity")
		return
	}
	if err := h.mgr.UpdateAgent(patch); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if err := h.persistAgent(); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"in-memory updated; yaml save failed: "+err.Error())
		return
	}
	h.logger.Info("providersmgmt: agent patched",
		zap.Bool("primary_changed", body.Primary != nil),
		zap.Bool("fallback_chain_changed", body.FallbackChain != nil),
		zap.Bool("max_tokens_changed", body.MaxTokens != nil),
		zap.Bool("temperature_changed", body.Temperature != nil),
		zap.Bool("context_window_changed", body.ContextWindow != nil),
		zap.Bool("max_turns_changed", body.MaxTurns != nil),
		zap.Bool("max_tool_calls_changed", body.MaxToolCalls != nil),
		zap.Bool("thinking_intensity_changed", body.ThinkingIntensity != nil),
	)
	writeSuccess(w, http.StatusOK, h.mgr.AgentSnapshot())
}

// ---------- persistence helpers ----------

func (h *Handler) persistProviderCreds(name string) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	llmCfg := h.mgr.CurrentConfig()
	agentCfg := h.mgr.CurrentAgent()
	provCfg, ok := llmCfg.Providers[name]
	if !ok {
		return fmt.Errorf("provider %q vanished from in-memory state", name)
	}
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		if err := f.SetProviderCreds(name, provCfg); err != nil {
			return err
		}
		// Provider-level disable may have removed chain entries —
		// mirror the new agent config to yaml so the file stays in sync.
		return f.SetAgent(agentCfg)
	})
}

func (h *Handler) persistEndpoint(provName, epName string) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	llmCfg := h.mgr.CurrentConfig()
	agentCfg := h.mgr.CurrentAgent()
	provCfg, ok := llmCfg.Providers[provName]
	if !ok {
		return fmt.Errorf("provider %q vanished", provName)
	}
	ep, ok := provCfg.Endpoints[epName]
	if !ok {
		return fmt.Errorf("endpoint %s.%s vanished", provName, epName)
	}
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		if err := f.SetEndpoint(provName, epName, ep); err != nil {
			return err
		}
		// Endpoint-level disable may have removed this entry from
		// chain — mirror the new agent config to yaml.
		return f.SetAgent(agentCfg)
	})
}

func (h *Handler) persistEndpointDeletion(provName, epName string) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	agentCfg := h.mgr.CurrentAgent()
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		if err := f.RemoveEndpoint(provName, epName); err != nil {
			return err
		}
		// Mirror auto-chain-removal: rewrite agent to match in-mem state.
		return f.SetAgent(agentCfg)
	})
}

func (h *Handler) persistAgent() error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	agentCfg := h.mgr.CurrentAgent()
	return mutateYAML(h.cfgPath, func(f *persist.File) error {
		return f.SetAgent(agentCfg)
	})
}

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

// ---------- response helpers ----------

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
