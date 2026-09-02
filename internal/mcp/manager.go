package mcp

import (
	"context"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
)

// Manager connects to the configured MCP servers once at startup, discovers
// their tools, and holds the sessions open for the process lifetime. The
// discovered tools are handed to the tool registry so every tool pool picks
// them up through NewToolPool's mcp slot.
//
// A server that fails to connect or list tools is skipped with a WARN — one
// bad server never blocks the others, and never blocks startup. Load is not
// safe for concurrent use with itself; call it once.
type Manager struct {
	servers   []config.MCPServerConfig
	connector Connector
	logger    *zap.Logger

	sessions []Session
	loaded   []tool.Tool
}

// NewManager builds a Manager for the given server configs. A nil connector
// uses the mark3labs/mcp-go transport implementation; tests pass a fake. A
// nil logger is tolerated (no-op).
func NewManager(servers []config.MCPServerConfig, connector Connector, logger *zap.Logger) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger = logger.Named("mcp")
	if connector == nil {
		connector = newMark3labsConnector(logger)
	}
	return &Manager{
		servers:   servers,
		connector: connector,
		logger:    logger,
	}
}

// Load connects to every enabled server and returns the discovered tools,
// each exposed under its "<server>__<remoteName>" namespaced name. The
// result is also cached (see Tools). Safe to call with no servers configured
// — it returns nil and opens nothing.
func (m *Manager) Load(ctx context.Context) []tool.Tool {
	var out []tool.Tool
	seen := make(map[string]bool, len(m.servers))

	for _, sc := range m.servers {
		name := strings.TrimSpace(sc.Name)
		if sc.Disabled {
			m.logger.Info("skipping disabled mcp server", zap.String("server", name))
			continue
		}
		if name == "" {
			m.logger.Warn("skipping mcp server with empty name")
			continue
		}
		if seen[name] {
			m.logger.Warn("skipping mcp server with duplicate name", zap.String("server", name))
			continue
		}
		seen[name] = true

		sess, err := m.connector.Connect(ctx, toServerConfig(sc))
		if err != nil {
			m.logger.Warn("mcp server connect failed — skipping",
				zap.String("server", name),
				zap.String("transport", transportOf(sc)),
				zap.Error(err))
			continue
		}

		specs, err := sess.ListTools(ctx)
		if err != nil {
			m.logger.Warn("mcp server list-tools failed — skipping",
				zap.String("server", name),
				zap.Error(err))
			_ = sess.Close()
			continue
		}

		deny := toSet(sc.Deny)
		n := 0
		for _, spec := range specs {
			if strings.TrimSpace(spec.Name) == "" {
				continue
			}
			if deny[spec.Name] {
				continue
			}
			out = append(out, newRemoteTool(name, spec, sess, m.logger))
			n++
		}

		m.sessions = append(m.sessions, sess)
		m.logger.Info("mcp server connected",
			zap.String("server", name),
			zap.Int("tools", n),
			zap.Int("advertised", len(specs)))
	}

	m.loaded = out
	return out
}

// Tools returns the tools discovered by the last Load call.
func (m *Manager) Tools() []tool.Tool { return m.loaded }

// Close shuts down every open session (and, for stdio servers, their
// subprocesses). It returns the first error encountered but always attempts
// to close all sessions. Safe to call even if Load was never called.
func (m *Manager) Close() error {
	var firstErr error
	for _, s := range m.sessions {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.sessions = nil
	return firstErr
}

// toServerConfig projects the app config type onto the transport-agnostic
// ServerConfig the Connector consumes.
func toServerConfig(sc config.MCPServerConfig) ServerConfig {
	return ServerConfig{
		Name:      strings.TrimSpace(sc.Name),
		Transport: transportOf(sc),
		Command:   sc.Command,
		Args:      sc.Args,
		Env:       sc.Env,
		URL:       sc.URL,
		Headers:   sc.Headers,
	}
}

// transportOf returns the normalized transport, defaulting empty to "stdio".
func transportOf(sc config.MCPServerConfig) string {
	t := strings.ToLower(strings.TrimSpace(sc.Transport))
	if t == "" {
		return "stdio"
	}
	return t
}

func toSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
