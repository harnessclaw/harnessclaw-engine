package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpspec "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

// clientName / clientVersion identify this engine to MCP servers during
// the initialize handshake. Some servers log or gate on it, so it is a
// stable string rather than the build version.
const (
	clientName    = "harnessclaw-engine"
	clientVersion = "0.1.0"
)

// closeGracePeriod is how long we wait for a child to exit on its own
// after its stdin is closed, before killing it.
//
// This exists because mcp-go's stdio Close() ends in cmd.Wait() with no
// timeout and no kill: it shuts stdin and then blocks until the child
// decides to exit. A server that ignores EOF — or one already wedged —
// blocks that call forever, which would hang engine shutdown and, on the
// connect-failure path, engine startup. So we bound it and kill.
const closeGracePeriod = 5 * time.Second

// ToolDescriptor is one tool exposed by one MCP server, flattened into
// the shape the tool bridge needs. It carries no mcp-go types so callers
// never take a transitive dependency on the SDK.
type ToolDescriptor struct {
	// Server is the configured server name this tool came from. Used to
	// namespace the engine-facing tool name and to route CallTool back to
	// the right connection.
	Server string

	// Name is the tool name as the MCP server calls it — NOT the
	// namespaced name the LLM sees. Pass this back to CallTool verbatim.
	Name string

	// Description is the server's own description, forwarded to the LLM
	// unchanged.
	Description string

	// InputSchema is the tool's JSON Schema as a decoded map, ready to
	// hand to the provider as-is.
	InputSchema map[string]any

	// ReadOnly mirrors the server's readOnlyHint annotation. Absent hint
	// means false: an unannotated MCP tool is treated as write-capable so
	// it does NOT slip through the permission pipeline's
	// ReadOnlyAutoAllow step, and is not eligible for parallel dispatch.
	ReadOnly bool
}

// CallResult is a completed tool call, already collapsed to the text the
// engine puts in a tool_result block.
type CallResult struct {
	// Text is every text content block from the server, joined by
	// newlines. Non-text blocks (image / audio / embedded resource) are
	// summarised rather than dropped silently — see collapseContent.
	Text string

	// IsError is the server's own isError flag: the tool ran and reported
	// failure. Distinct from a transport error, which CallTool returns as
	// a Go error instead.
	IsError bool
}

// serverClient owns one MCP server connection and the tools it
// advertised at connect time.
//
// Concurrency: the engine dispatches tools in parallel, so every field
// behind mu can be touched from several goroutines. mu is held across
// the whole connect sequence (spawn + initialize + tools/list) so two
// simultaneous first-calls cannot spawn two child processes.
type serverClient struct {
	name   string
	cfg    config.MCPServerConfig
	logger *zap.Logger

	mu    sync.Mutex
	inner *mcpclient.Client // nil when not connected
	tools []ToolDescriptor

	// proc is the child process behind inner, when there is one. Held so
	// a child that ignores stdin close can be killed — mcp-go does not
	// expose its own exec.Cmd. Nil for non-stdio transports (in-process
	// test dialers).
	proc *exec.Cmd

	// dial opens a transport to the server. Injectable so tests can
	// drive the full connect / call / reconnect logic against an
	// in-process MCP server instead of spawning a child process — the
	// reconnect path in particular is hard to exercise any other way,
	// and it is the part most likely to break. Nil means stdio.
	dial dialer
}

// dialer returns an mcp client and, for process-backed transports, the
// child it is talking to. connectLocked calls Start on the client, which
// mcp-go documents as idempotent.
//
// The *exec.Cmd is returned rather than discarded so the caller can kill
// a child that will not exit on its own; see closeGracePeriod.
type dialer func() (*mcpclient.Client, *exec.Cmd, error)

// stdioDialer is the production dialer: spawn the configured command and
// speak JSON-RPC over its stdin/stdout.
//
// It supplies its own CommandFunc purely to keep a handle on the spawned
// process. Behaviour is otherwise identical to NewStdioMCPClient — same
// command, args and environment.
func stdioDialer(cfg config.MCPServerConfig, env []string) dialer {
	return func() (*mcpclient.Client, *exec.Cmd, error) {
		var spawned *exec.Cmd
		c, err := mcpclient.NewStdioMCPClientWithOptions(
			cfg.Command, env, cfg.Args,
			mcptransport.WithCommandFunc(func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, command, args...)
				cmd.Env = env
				spawned = cmd
				return cmd, nil
			}),
		)
		if err != nil {
			return nil, spawned, err
		}
		return c, spawned, nil
	}
}

// connect spawns the server and completes the MCP handshake. Safe to
// call when already connected — it is then a no-op, which is what makes
// it usable as a lazy reconnect from the call path.
//
// timeout is the budget for the WHOLE handshake (spawn, start,
// initialize, tools/list), not per step. Startup waits on this, so a
// per-step budget would multiply into a boot delay — see
// Manager.connectTimeoutFor.
func (s *serverClient) connect(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectLocked(ctx, timeout)
}

func (s *serverClient) connectLocked(ctx context.Context, timeout time.Duration) error {
	if s.inner != nil {
		return nil
	}

	dial := s.dial
	if dial == nil {
		dial = stdioDialer(s.cfg, s.envSlice())
	}

	// A single deadline spanning the whole handshake. Previously each
	// step got its own `timeout`, so a server that accepted the spawn but
	// answered nothing burned 2x the budget — with the engine's 120s tool
	// timeout as the default that was a four-minute boot stall, and the
	// desktop app just looks hung while it waits for the WebSocket.
	hsCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	inner, proc, err := dial()
	if err != nil {
		// A partially-spawned child must not be left behind.
		killProcess(proc)
		return fmt.Errorf("spawn %q: %w", s.cfg.Command, err)
	}

	// The stdio dialer already started its transport; Start is
	// idempotent, so calling it unconditionally keeps both dialers on one
	// path instead of making each one responsible for its own lifecycle.
	if err := inner.Start(hsCtx); err != nil {
		s.shutdown(inner, proc, 0)
		return fmt.Errorf("start transport: %w", err)
	}

	if _, err := inner.Initialize(hsCtx, mcpspec.InitializeRequest{
		Params: mcpspec.InitializeParams{
			ProtocolVersion: mcpspec.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcpspec.Implementation{
				Name:    clientName,
				Version: clientVersion,
			},
		},
	}); err != nil {
		// Close the child we just spawned; leaking it would leave an
		// orphan process per failed attempt, and the lazy-reconnect path
		// retries on every tool call. shutdown (not Close) because a
		// server that failed the handshake is exactly the kind that also
		// ignores stdin close.
		s.shutdown(inner, proc, 0)
		return fmt.Errorf("initialize: %w", err)
	}

	listed, err := inner.ListTools(hsCtx, mcpspec.ListToolsRequest{})
	if err != nil {
		s.shutdown(inner, proc, 0)
		return fmt.Errorf("tools/list: %w", err)
	}

	tools := make([]ToolDescriptor, 0, len(listed.Tools))
	for _, t := range listed.Tools {
		schema, err := schemaOf(t)
		if err != nil {
			// One unreadable schema must not cost us the whole server —
			// the other tools are still usable. WARN so the gap is
			// visible rather than looking like the server never offered
			// the tool.
			s.logger.Warn("mcp: skipping tool with undecodable input schema",
				zap.String("server", s.name),
				zap.String("tool", t.Name),
				zap.Error(err))
			continue
		}
		tools = append(tools, ToolDescriptor{
			Server:      s.name,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
			ReadOnly:    t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint,
		})
	}

	s.inner = inner
	s.proc = proc
	s.tools = tools
	return nil
}

// shutdown closes a client without ever blocking indefinitely: it gives
// the child closeGracePeriod to exit after stdin closes, then kills it.
//
// mcp-go's stdio Close() ends in cmd.Wait(), so on an uncooperative child
// it never returns. The Close goroutine finishes once the kill makes
// Wait() return, so nothing is leaked.
func (s *serverClient) shutdown(inner *mcpclient.Client, proc *exec.Cmd, grace time.Duration) {
	if inner == nil {
		killProcess(proc)
		return
	}

	// Close still runs even when we are not going to wait for it: it is
	// what shuts stdin and lets cmd.Wait() return once the child is gone,
	// so the goroutine always completes.
	done := make(chan error, 1)
	go func() { done <- inner.Close() }()

	if grace <= 0 {
		// Handshake already failed, so this process has forfeited a
		// polite exit. Killing now keeps startup snappy instead of
		// spending the grace period on a server we have written off.
		killProcess(proc)
		return
	}

	select {
	case err := <-done:
		if err != nil && s.logger != nil {
			s.logger.Debug("mcp: error closing connection",
				zap.String("server", s.name), zap.Error(err))
		}
	case <-time.After(grace):
		if s.logger != nil {
			s.logger.Warn("mcp: server did not exit after stdin close — killing it",
				zap.String("server", s.name),
				zap.Duration("grace", grace))
		}
		killProcess(proc)
	}
}

// killProcess terminates a spawned child if it is still running.
// Tolerates every nil along the way, since non-stdio transports have no
// process and a failed spawn may have none either.
func killProcess(proc *exec.Cmd) {
	if proc == nil || proc.Process == nil {
		return
	}
	_ = proc.Process.Kill()
}

// call invokes one tool. A non-nil error means the request itself failed
// (transport dead, timeout, protocol error) and the connection has been
// dropped so the next call reconnects. A tool that ran and failed comes
// back as CallResult.IsError instead, with no error.
func (s *serverClient) call(ctx context.Context, toolName string, args map[string]any, connectTimeout, requestTimeout time.Duration) (*CallResult, error) {
	s.mu.Lock()
	if err := s.connectLocked(ctx, connectTimeout); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	inner := s.inner
	s.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := inner.CallTool(callCtx, mcpspec.CallToolRequest{
		Params: mcpspec.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	})
	if err != nil {
		// mcp-go reports tool-level failure through res.IsError, so a Go
		// error here is the request not completing at all. Treat the
		// connection as suspect and drop it: a long-lived desktop
		// session outlives plenty of child processes, and without this
		// every later call to a dead server fails forever.
		//
		// Keyed on the client we actually used. Tools dispatch in
		// parallel, so an unconditional drop let this happen: A and B
		// both hold C1; A fails and reconnects to C2; B then fails on
		// the now-closed C1 and its drop closes A's healthy C2. One
		// stale error would take out a fresh connection.
		s.dropIfCurrent(inner)
		return nil, fmt.Errorf("call %q: %w", toolName, err)
	}

	return &CallResult{
		Text:    collapseContent(res.Content, res.StructuredContent),
		IsError: res.IsError,
	}, nil
}

// dropIfCurrent closes and forgets the child only if it is still the one
// the caller was using, so a late failure from a superseded connection
// cannot take down its replacement. Idempotent; nil `used` is a no-op.
func (s *serverClient) dropIfCurrent(used *mcpclient.Client) {
	if used == nil {
		return
	}
	// Compare and close under ONE lock hold. Checking with the lock, then
	// releasing it before closing, reintroduces the very race this
	// guards: the connection can be replaced in that window and we would
	// close the replacement.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner != used {
		// Already replaced, or already dropped, by someone else.
		return
	}
	s.closeInnerLocked()
}

// dropConnection closes and forgets the child so the next call spawns a
// fresh one. Idempotent.
//
// Prefer dropIfCurrent from the call path; this unconditional form is for
// shutdown and for tests that mean "drop whatever is there".
func (s *serverClient) dropConnection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeInnerLocked()
}

// closeInnerLocked closes the current child and clears the field. Caller
// must hold s.mu.
func (s *serverClient) closeInnerLocked() {
	if s.inner == nil {
		return
	}
	s.shutdown(s.inner, s.proc, closeGracePeriod)
	s.inner = nil
	s.proc = nil
	// Keep s.tools: the descriptors were valid when we got them, and
	// dropping them here would make the tool disappear from the LLM's
	// schema mid-session on a transient failure. A reconnect refreshes
	// them.
}

// close shuts the child down for good. Never blocks longer than
// closeGracePeriod.
func (s *serverClient) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeInnerLocked()
}

// snapshotTools returns a copy so callers cannot mutate our slice.
func (s *serverClient) snapshotTools() []ToolDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ToolDescriptor, len(s.tools))
	copy(out, s.tools)
	return out
}

// envSlice renders the configured env map as the KEY=VALUE slice
// exec.Cmd wants, layered over the engine's own environment so a server
// still sees PATH, HOME and friends.
//
// Key case is preserved exactly as written in YAML — config.Load goes
// out of its way to recover it (see config.applyRawMCPEnv) because
// environment variables are case-sensitive and a server handed
// `github_token` instead of `GITHUB_TOKEN` starts fine and then fails to
// authenticate.
//
// Sorted for reproducibility: process env order is not semantically
// meaningful, but a stable order keeps test assertions and any debug
// dump deterministic.
func (s *serverClient) envSlice() []string {
	if len(s.cfg.Env) == 0 {
		return os.Environ()
	}
	keys := make([]string, 0, len(s.cfg.Env))
	for k := range s.cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	base := os.Environ()
	out := make([]string, 0, len(base)+len(keys))
	out = append(out, base...)
	for _, k := range keys {
		out = append(out, k+"="+s.cfg.Env[k])
	}
	return out
}

// schemaOf extracts a tool's input schema as a decoded map.
//
// mcp.Tool carries the schema two ways: RawInputSchema (arbitrary JSON
// Schema supplied verbatim) takes precedence over the structured
// InputSchema when set, matching mcp-go's own marshalling rule.
func schemaOf(t mcpspec.Tool) (map[string]any, error) {
	raw := t.RawInputSchema
	if len(raw) == 0 {
		encoded, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal input schema: %w", err)
		}
		raw = encoded
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}
	if out == nil {
		// A tool taking no arguments still needs a valid object schema
		// or providers reject the tool definition outright.
		out = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return out, nil
}

// collapseContent flattens MCP content blocks into the single string the
// engine puts in a tool_result.
//
// Text blocks are joined verbatim. Non-text blocks are replaced with a
// short marker rather than dropped, so an LLM that gets an image back
// learns something happened instead of seeing an empty result and
// retrying forever. structured falls back in when a server returned only
// structuredContent (allowed by spec, though servers SHOULD also send
// equivalent text).
func collapseContent(content []mcpspec.Content, structured any) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		switch v := c.(type) {
		case mcpspec.TextContent:
			parts = append(parts, v.Text)
		case *mcpspec.TextContent:
			parts = append(parts, v.Text)
		default:
			parts = append(parts, fmt.Sprintf("[non-text content: %T]", c))
		}
	}

	if len(parts) == 0 && structured != nil {
		if encoded, err := json.Marshal(structured); err == nil {
			return string(encoded)
		}
	}

	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}
