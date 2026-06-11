package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/services/api/agentmgmt"
)

// ServerConfig holds console API server settings.
type ServerConfig struct {
	Host string
	Port int
}

// Server is the Console management HTTP API server.
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
}

// NewServer creates a console API server and registers all handlers.
//
// metricsHandler, if non-nil, is mounted at /api/v1/sessions/ so the
// session metrics dashboard can fetch per-session token / latency
// stats over the same port as the rest of the management API.
//
// modelsHandler, if non-nil, is mounted at /api/v1/models for the
// model + provider capability registry consumed by the client UI.
//
// providersHandler, if non-nil, is mounted at /api/v1/providers and
// /api/v1/agent for runtime provider / agent-routing management.
// Only non-nil when the server runs in multi-provider mode —
// single-provider deployments don't expose runtime mutation.
//
// toolsHandler, if non-nil, is mounted at /api/v1/tools for per-tool
// credential management with hot-reload + yaml persistence.
//
// videoGenHandler, if non-nil, is mounted at /api/v1/videogen for runtime
// video-generation provider management (hot-reload + yaml persistence). It
// shares the same live Source the video tools read from.
//
// artifactsHandler, if non-nil, is mounted at /api/v1/artifacts/ so the
// Electron client can fetch raw binary artifact bytes for preview /
// download without going through the LLM tool path.
//
// capabilitiesHandler, if non-nil, is mounted at /api/v1/agent/capabilities
// for the resolved active-model SupportsFlags + derived capability buckets.
// Mounted ahead of /api/v1/agent so the more specific path wins in
// http.ServeMux (longest pattern match).
func NewServer(cfg ServerConfig, agentSvc *agentmgmt.AgentService, metricsHandler http.Handler, modelsHandler http.Handler, providersHandler http.Handler, toolsHandler http.Handler, videoGenHandler http.Handler, artifactsHandler http.Handler, capabilitiesHandler http.Handler, logger *zap.Logger) *Server {
	mux := http.NewServeMux()

	// Register agent management routes
	agentHandler := NewAgentHandler(agentSvc, logger)
	agentHandler.RegisterRoutes(mux)

	// Session metrics: GET /api/v1/sessions/{id}/metrics
	if metricsHandler != nil {
		mux.Handle("/api/v1/sessions/", metricsHandler)
	}

	// Model registry: GET /api/v1/models, GET /api/v1/models/{provider}/{model_id}
	if modelsHandler != nil {
		mux.Handle("/api/v1/models", modelsHandler)
		mux.Handle("/api/v1/models/", modelsHandler)
	}

	// Agent capabilities: GET /api/v1/agent/capabilities. Mounted
	// BEFORE the providers handler's /api/v1/agent/ catch so the
	// more-specific path wins in http.ServeMux. Computes the same
	// SupportsFlags the multimodal Gate uses (override-aware).
	if capabilitiesHandler != nil {
		mux.Handle("/api/v1/agent/capabilities", capabilitiesHandler)
	}

	// Providers management API: nested under /api/v1/providers plus
	// /api/v1/agent (agent-level routing config — primary +
	// fallback_chain + per-call defaults — sits at the LLM root, not
	// under a specific provider).
	if providersHandler != nil {
		mux.Handle("/api/v1/providers", providersHandler)
		mux.Handle("/api/v1/providers/", providersHandler)
		mux.Handle("/api/v1/agent", providersHandler)
		mux.Handle("/api/v1/agent/", providersHandler)
	}

	// Tools management API: GET/PATCH per-tool credentials with
	// hot-reload + yaml persistence. Mounted at /api/v1/tools.
	if toolsHandler != nil {
		mux.Handle("/api/v1/tools", toolsHandler)
		mux.Handle("/api/v1/tools/", toolsHandler)
	}

	// Video generation management API: GET/PATCH videogen providers with
	// hot-reload + yaml persistence. Mounted at /api/v1/videogen.
	if videoGenHandler != nil {
		mux.Handle("/api/v1/videogen", videoGenHandler)
		mux.Handle("/api/v1/videogen/", videoGenHandler)
	}

	// Artifact content streaming: GET /api/v1/artifacts/{id}/content
	// Returns the raw bytes (post hybrid-store hydration) so the client
	// can save to a temp file and reuse the existing files:read +
	// mammoth/pdf-parse rich-preview pipeline.
	if artifactsHandler != nil {
		mux.Handle("/api/v1/artifacts/", artifactsHandler)
	}

	// Health check
	mux.HandleFunc("GET /console/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeSuccess(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	return &Server{
		httpServer: &http.Server{
			Addr:         addr,
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Start starts the console API server. Blocks until the server stops.
func (s *Server) Start() error {
	s.logger.Info("console API server starting", zap.String("addr", s.httpServer.Addr))
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("console API listen: %w", err)
	}
	return s.httpServer.Serve(ln)
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("console API server stopping")
	return s.httpServer.Shutdown(ctx)
}
