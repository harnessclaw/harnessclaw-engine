// Package agentcapabilities serves the resolved capability matrix
// for the active agent chain. Returns a single, fully-derived
// snapshot so the client doesn't need to re-implement the manifest
// vs endpoint-override merge logic.
package agentcapabilities

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider/registry"
)

// ActiveInfoProvider mirrors router.ModelInfoProvider so we can share
// the same bridge instance — the same SupportsFlags shows up here as
// in the gate, by construction.
type ActiveInfoProvider interface {
	ActiveModelKey() string
	ActiveSupports() registry.SupportsFlags
}

// Handler implements http.Handler for GET /api/v1/agent/capabilities.
type Handler struct {
	info   ActiveInfoProvider
	logger *zap.Logger
}

// New constructs a Handler.
func New(info ActiveInfoProvider, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{info: info, logger: logger}
}

// ServeHTTP returns the active model's resolved supports + derived
// capability buckets. Only GET is supported.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET supported")
		return
	}
	key := h.info.ActiveModelKey()
	s := h.info.ActiveSupports()
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"model_key":    key,
			"supports":     supportsToJSON(s),
			"capabilities": registry.DeriveCapabilities(s),
		},
	})
}

// supportsToJSON renders SupportsFlags as a snake_case JSON map
// matching /api/v1/models supports.* shape, so the client can use
// the same RegistryModelSupports interface for both endpoints.
func supportsToJSON(s registry.SupportsFlags) map[string]any {
	return map[string]any{
		"vision":                    s.Vision,
		"pdf_input":                 s.PDFInput,
		"audio_input":               s.AudioInput,
		"video_input":               s.VideoInput,
		"audio_output":              s.AudioOutput,
		"image_generation":          s.ImageGeneration,
		"streaming":                 s.Streaming,
		"system_messages":           s.SystemMessages,
		"structured_output":         s.StructuredOutput,
		"function_calling":          s.FunctionCalling,
		"parallel_function_calling": s.ParallelFunctionCalling,
		"tool_choice":               s.ToolChoice,
		"computer_use":              s.ComputerUse,
		"web_search":                s.WebSearch,
		"reasoning":                 s.Reasoning,
		"reasoning_can_disable":     s.ReasoningCanDisable,
		"prompt_caching":            s.PromptCaching,
		"explicit_cache_control":    s.ExplicitCacheControl,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
