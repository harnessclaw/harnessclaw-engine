package mcp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

// DefaultRequestTimeout caps one MCP tool call when NewManager is given a
// zero request timeout. It is a fallback for callers that do not care —
// cmd/server passes engine.tool_timeout, so this value does not apply in
// production.
const DefaultRequestTimeout = 60 * time.Second

// DefaultConnectTimeout is the budget for a whole server handshake
// (spawn, initialize, tools/list) when a server sets no explicit timeout.
//
// Deliberately much shorter than DefaultRequestTimeout, and deliberately
// NOT engine.tool_timeout: startup waits on this, and the desktop app in
// turn waits on the engine's WebSocket, so a slow server here reads to the
// user as an app that will not launch. A tool CALL can afford to be slow;
// a handshake blocking boot cannot.
//
// A server that genuinely needs longer — a cold `npx` that installs on
// first run — should say so with an explicit per-server `timeout`, which
// overrides this. That keeps the fast-fail default without capping the
// operator who knows their server is slow.
const DefaultConnectTimeout = 20 * time.Second

// ErrUnknownServer is returned when a call names a server this Manager
// never connected. It means the tool pool and the Manager have drifted,
// which is a wiring bug rather than a user error.
var ErrUnknownServer = fmt.Errorf("mcp: unknown server")

// Manager owns every MCP server connection for the process.
//
// Lifecycle: construct once at startup, Connect, read Tools() to build
// the tool pool, then Close on shutdown. Connect is best-effort by
// design — a server that fails to start contributes no tools and logs a
// WARN, but never stops the engine from booting. MCP is additive; a
// broken filesystem server must not cost the user their chat.
type Manager struct {
	logger         *zap.Logger
	defaultTimeout time.Duration
	connectTimeout time.Duration

	mu      sync.RWMutex
	servers map[string]*serverClient
}

// NewManager builds an empty Manager.
//
// requestTimeout bounds one tool call; connectTimeout bounds one whole
// handshake. Zero for either means the corresponding Default*. They are
// separate parameters because they answer different questions — how long
// may a tool run, versus how long may startup wait — and conflating them
// is what made engine boot stall on a bad server.
func NewManager(logger *zap.Logger, requestTimeout, connectTimeout time.Duration) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	return &Manager{
		logger:         logger,
		defaultTimeout: requestTimeout,
		connectTimeout: connectTimeout,
		servers:        make(map[string]*serverClient),
	}
}

// Connect starts every enabled server in cfgs, concurrently, and returns
// once all attempts have settled.
//
// Disabled entries are skipped outright. Failures are logged and
// swallowed: callers get a usable Manager containing whatever connected.
//
// This BLOCKS, and callers run it during startup, so the worst-case delay
// is one connectTimeoutFor — not the sum across servers, since they dial
// in parallel. With defaults that is DefaultConnectTimeout (20s), and a
// server that sets an explicit `timeout` raises its own bound only.
//
// Safe to call once. Calling it again with the same names is a no-op for
// already-connected servers.
func (m *Manager) Connect(ctx context.Context, cfgs map[string]config.MCPServerConfig) {
	// Sorted so startup logs read the same across runs — Go map order is
	// randomised, and interleaved connect logs are hard enough to follow
	// without that.
	names := make([]string, 0, len(cfgs))
	for name, c := range cfgs {
		if c.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		m.logger.Debug("mcp: no enabled servers configured")
		return
	}

	m.logger.Info("mcp: connecting servers", zap.Strings("servers", names))

	var wg sync.WaitGroup
	for _, name := range names {
		cfg := cfgs[name]

		m.mu.Lock()
		if _, exists := m.servers[name]; exists {
			m.mu.Unlock()
			continue
		}
		sc := &serverClient{name: name, cfg: cfg, logger: m.logger}
		sc.dial = stdioDialer(cfg, sc.envSlice())
		m.servers[name] = sc
		m.mu.Unlock()

		wg.Add(1)
		go func(name string, sc *serverClient, cfg config.MCPServerConfig) {
			defer wg.Done()

			started := time.Now()
			if err := sc.connect(ctx, m.connectTimeoutFor(cfg)); err != nil {
				// The entry stays registered, but be clear about what
				// that does and does not buy: a server that fails HERE
				// advertised no tools, so nothing it offers is in any
				// tool pool and no agent can call it. The lazy reconnect
				// in call() therefore cannot rescue it — it only covers
				// a server that connected once and died mid-session,
				// where the descriptors are already published.
				//
				// Recovering from a startup failure needs a re-dial that
				// also refreshes the pools, which is a follow-up. Until
				// then this requires an engine restart, and the log line
				// says so.
				m.logger.Warn("mcp: server failed to connect — its tools are unavailable until the engine restarts",
					zap.String("server", name),
					zap.String("command", cfg.Command),
					zap.Duration("elapsed", time.Since(started)),
					zap.Error(err))
				return
			}
			tools := sc.snapshotTools()
			m.logger.Info("mcp: server connected",
				zap.String("server", name),
				zap.Int("tools", len(tools)),
				zap.Strings("tool_names", toolNames(tools)),
				zap.Duration("elapsed", time.Since(started)))
		}(name, sc, cfg)
	}
	wg.Wait()
}

// Tools returns every tool across every connected server, sorted by
// (server, tool) so the resulting pool ordering is reproducible.
//
// Ordering matters more than it looks: these descriptors become tool
// schemas in the LLM request, and an unstable order would churn the
// prompt-cache prefix on every turn. tool.NewToolPool sorts again by
// engine-facing name, but starting from a deterministic slice keeps logs
// and tests stable too.
func (m *Manager) Tools() []ToolDescriptor {
	m.mu.RLock()
	servers := make([]*serverClient, 0, len(m.servers))
	for _, sc := range m.servers {
		servers = append(servers, sc)
	}
	m.mu.RUnlock()

	var out []ToolDescriptor
	for _, sc := range servers {
		out = append(out, sc.snapshotTools()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// CallTool routes a call to the named server. A non-nil error means the
// request did not complete; a tool that ran and failed returns a
// CallResult with IsError set.
func (m *Manager) CallTool(ctx context.Context, server, toolName string, args map[string]any) (*CallResult, error) {
	m.mu.RLock()
	sc, ok := m.servers[server]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, server)
	}
	return sc.call(ctx, toolName, args, m.connectTimeoutFor(sc.cfg), m.timeoutFor(sc.cfg))
}

// Close shuts every server down and returns once they are all gone.
//
// Servers close in parallel and each is individually bounded (see
// serverClient.shutdown), so total time is one grace period rather than
// one per stubborn server. Sequential closes would multiply: engine
// shutdown is on this path, and a shutdown that hangs is worse than one
// that kills a child a little early.
//
// The error return is retained for API compatibility and is always nil —
// per-server failures are logged, since there is nothing a caller could
// usefully do about a child that would not exit.
func (m *Manager) Close() error {
	m.mu.Lock()
	servers := m.servers
	m.servers = make(map[string]*serverClient)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, sc := range servers {
		wg.Add(1)
		go func(sc *serverClient) {
			defer wg.Done()
			sc.close()
		}(sc)
	}
	wg.Wait()
	return nil
}

// ServerNames lists registered servers in sorted order, connected or
// not. Used for status reporting.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// timeoutFor is the per-call budget.
func (m *Manager) timeoutFor(cfg config.MCPServerConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return m.defaultTimeout
}

// connectTimeoutFor is the per-handshake budget. An explicit per-server
// timeout wins here too: setting it is how an operator says "this server
// is slow to start, wait for it" without slowing every other server's
// contribution to boot time.
func (m *Manager) connectTimeoutFor(cfg config.MCPServerConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return m.connectTimeout
}

func toolNames(tools []ToolDescriptor) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
