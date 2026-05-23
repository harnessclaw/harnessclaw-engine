// Package toolsmgmt serves the runtime tools-management API.
//
// Scope: search-tool credentials (web_search, tavily_search). The
// design generalises to any tool whose runtime config is a flat
// credential map plus enabled flag, but this package currently
// only registers the two search backends.
//
// Routes:
//
//	GET    /api/v1/tools                # list manageable tools
//	GET    /api/v1/tools/{name}         # current state
//	PATCH  /api/v1/tools/{name}         # update enabled + config; hot-reload + yaml rewrite
//
// Mutations are dual-write: the live tool.Registry instance is
// swapped first (via Replace, atomic under registry mu), and the yaml
// is rewritten on success. If yaml persistence fails we restore the
// previous tool instance — the in-mem registry never desyncs from yaml.
package toolsmgmt

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	persistPkg "harnessclaw-go/internal/config/persist"
	"harnessclaw-go/internal/tool"
)

// Handler implements http.Handler for the tools management API.
type Handler struct {
	mu       sync.Mutex // serialises handlePatch's read-merge-apply-persist sequence
	registry *tool.Registry
	cfg      *config.Config
	cfgPath  string
	logger   *zap.Logger
}

// New constructs a handler. cfg is the live config struct so PATCH
// can merge partial updates into it. cfgPath is the absolute yaml
// path viper actually loaded.
func New(registry *tool.Registry, cfg *config.Config, cfgPath string, logger *zap.Logger) *Handler {
	return &Handler{registry: registry, cfg: cfg, cfgPath: cfgPath, logger: logger}
}

// ToolEntry is the wire-shape returned by GET endpoints.
type ToolEntry struct {
	Name             string         `json:"name"`              // yaml key, e.g. "web_search"
	RegisteredName   string         `json:"registered_name"`   // LLM-facing tool name, e.g. "web_search"
	Enabled          bool           `json:"enabled"`           // raw enabled flag
	Effective        bool           `json:"effective"`         // tool.IsEnabled() — true only when enabled AND credentials complete
	Config           map[string]any `json:"config"`            // current cfg fields (api_key plaintext, per design A/A/A/A/A)
	CredentialFields []string       `json:"credential_fields"` // which Config keys are credentials (UI hint)
}

// factory describes how to read the current cfg + build a new instance
// when PATCHing. One entry per manageable tool. Adding a new tool is:
// (1) add an entry here, (2) extend cfg.Tools, (3) cover with tests.
type factory struct {
	// registeredName is the tool.Name() returned by the constructed tool.
	registeredName string
	// credentialFields is which raw config keys count as credentials.
	credentialFields []string
	// snapshot reads the current cfg into the wire-shape map.
	snapshot func(*config.Config) map[string]any
	// apply takes the wire-shape map, validates required fields when
	// enabled, builds a fresh tool instance, and returns it (or error).
	// It does NOT mutate cfg — the caller does that after apply succeeds.
	apply func(raw map[string]any, logger *zap.Logger) (tool.Tool, config.ToolsConfig, error)
}

// factories is the per-yaml-key registry. Iterate in name order for
// stable list responses (sortedNames helper).
var factories = map[string]*factory{} // populated in init() — see factories.go

// snapshotEntry builds a ToolEntry for the current state of name.
func (h *Handler) snapshotEntry(name string) (ToolEntry, bool) {
	f, ok := factories[name]
	if !ok {
		return ToolEntry{}, false
	}
	cur := h.registry.Get(f.registeredName)
	enabled := false
	effective := false
	if cur != nil {
		enabled = enabledFromCfg(name, h.cfg)
		effective = cur.IsEnabled()
	}
	return ToolEntry{
		Name:             name,
		RegisteredName:   f.registeredName,
		Enabled:          enabled,
		Effective:        effective,
		Config:           f.snapshot(h.cfg),
		CredentialFields: f.credentialFields,
	}, true
}

// enabledFromCfg is the raw cfg.Tools.<X>.Enabled flag (NOT IsEnabled —
// the API caller wants to see what they wrote, not what the tool's
// IsEnabled gate decided after credential check).
func enabledFromCfg(name string, cfg *config.Config) bool {
	switch name {
	case "web_search":
		return cfg.Tools.WebSearch.Enabled
	case "tavily_search":
		return cfg.Tools.TavilySearch.Enabled
	}
	return false
}

// sortedNames returns the manageable tool names in stable order for list responses.
func sortedNames() []string {
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	// 2-element slice; manual sort is fine and avoids a "sort" import dependency.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i] > out[j] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// ServeHTTP dispatches GET/PATCH on the tools API surface.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /api/v1/tools             → list
	// /api/v1/tools/{name}      → get / patch
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/tools")
	trimmed = strings.TrimPrefix(trimmed, "/")
	trimmed = strings.TrimSuffix(trimmed, "/")

	if trimmed == "" {
		// Collection endpoint.
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
				"only GET supported on /api/v1/tools")
			return
		}
		out := make([]ToolEntry, 0, len(factories))
		for _, name := range sortedNames() {
			if entry, ok := h.snapshotEntry(name); ok {
				out = append(out, entry)
			}
		}
		writeSuccess(w, http.StatusOK, map[string]any{"tools": out})
		return
	}

	// Single-tool endpoint.
	name := trimmed
	if _, ok := factories[name]; !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown tool: "+name)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entry, _ := h.snapshotEntry(name)
		writeSuccess(w, http.StatusOK, entry)
	case http.MethodPatch:
		h.handlePatch(w, r, name)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"only GET/PATCH supported")
	}
}

// ---------- PATCH helpers ----------

// patchRequest is the JSON body shape. Both fields are optional;
// omitted means "keep current value". config is a partial map merged
// over the current snapshot, NOT a full replacement.
type patchRequest struct {
	Enabled *bool          `json:"enabled,omitempty"`
	Config  map[string]any `json:"config,omitempty"`
}

// handlePatch builds the merged config, validates, hot-swaps, and persists.
// Caller has already resolved name and confirmed factories[name] exists.
func (h *Handler) handlePatch(w http.ResponseWriter, r *http.Request, name string) {
	var body patchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}

	// Serialise the entire read-merge-apply-persist sequence so concurrent
	// PATCH requests can't race on cfg.Tools. The registry.Replace itself
	// is already atomic under Registry.mu, but the cfg snapshot + the
	// in-memory swap + yaml write must be one logical unit.
	h.mu.Lock()
	defer h.mu.Unlock()

	f := factories[name]
	// Start from current cfg, overlay body.
	merged := f.snapshot(h.cfg)
	if body.Enabled != nil {
		merged["enabled"] = *body.Enabled
	}
	for k, v := range body.Config {
		merged[k] = v
	}

	// Build new tool instance + new cfg chunk. Validation lives in apply.
	newTool, newToolsChunk, err := f.apply(merged, h.logger)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	// Snapshot the current registry entry so we can roll back on persist failure.
	prevTool := h.registry.Get(f.registeredName)
	if prevTool == nil {
		writeError(w, http.StatusInternalServerError, "registry_missing",
			"tool "+f.registeredName+" not registered at startup; restart required")
		return
	}

	// Hot-swap registry first — atomic under Registry mu.
	if err := h.registry.Replace(f.registeredName, newTool); err != nil {
		writeError(w, http.StatusInternalServerError, "hot_reload_failed", err.Error())
		return
	}

	// Apply to in-mem cfg. We swap only the relevant sub-struct field so
	// other tools' settings are untouched.
	prevCfgChunk := h.cfg.Tools
	applyToolsChunk(h.cfg, name, newToolsChunk)

	// Persist yaml. On failure, roll back both registry and in-mem cfg.
	if err := h.persist(name, merged); err != nil {
		if rbErr := h.registry.Replace(f.registeredName, prevTool); rbErr != nil {
			h.logger.Error("registry rollback failed after persist error",
				zap.String("name", name),
				zap.Error(rbErr),
				zap.NamedError("original_error", err),
			)
		}
		h.cfg.Tools = prevCfgChunk
		writeError(w, http.StatusInternalServerError, "persist_failed",
			"hot-swap rolled back: "+err.Error())
		return
	}

	entry, _ := h.snapshotEntry(name)
	writeSuccess(w, http.StatusOK, entry)
	h.logger.Info("tool config updated",
		zap.String("name", name),
		zap.Bool("enabled", merged["enabled"] == true),
	)
}

// applyToolsChunk overwrites only the named tool's sub-struct in cfg.Tools,
// leaving the other tools' fields intact. Because config.ToolsConfig is a
// flat struct of per-tool configs, we can't just `cfg.Tools = chunk` —
// we'd zero out everything else. Handle each known tool here.
func applyToolsChunk(cfg *config.Config, name string, chunk config.ToolsConfig) {
	switch name {
	case "web_search":
		cfg.Tools.WebSearch = chunk.WebSearch
	case "tavily_search":
		cfg.Tools.TavilySearch = chunk.TavilySearch
	}
}

// persist round-trips the merged config through the yaml writer.
// Pure helper — handler.go owns rollback semantics around this call.
func (h *Handler) persist(name string, raw map[string]any) error {
	if h.cfgPath == "" {
		return errors.New("cfg path not configured")
	}
	return mutateYAML(h.cfgPath, func(f *persistFile) error {
		return f.SetToolConfig(name, raw)
	})
}

// mutateYAML / persistFile shim — wraps internal/config/persist so the
// rest of the handler doesn't need to import persist directly throughout.
type persistFile = persistPkg.File

func mutateYAML(path string, fn func(f *persistFile) error) error {
	f, err := persistPkg.Load(path)
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
