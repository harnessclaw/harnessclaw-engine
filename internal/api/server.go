package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/agent"
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
func NewServer(cfg ServerConfig, agentSvc *agent.AgentService, logger *zap.Logger) *Server {
	mux := http.NewServeMux()

	// Register agent management routes
	agentHandler := NewAgentHandler(agentSvc, logger)
	agentHandler.RegisterRoutes(mux)

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
