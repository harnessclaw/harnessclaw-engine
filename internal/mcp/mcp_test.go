package mcp

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpspec "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

// fakeServer builds an in-process MCP server exposing two tools: a
// read-only "lookup" and an unannotated "mutate" that echoes its args.
// Using a real server (rather than a stub client) means the handshake,
// tools/list decoding and tools/call round-trip are all genuinely
// exercised.
func fakeServer(t *testing.T) *mcpserver.MCPServer {
	t.Helper()
	srv := mcpserver.NewMCPServer("fake", "1.0.0")

	readOnly := true
	srv.AddTool(
		mcpspec.Tool{
			Name:        "lookup",
			Description: "Look something up.",
			InputSchema: mcpspec.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{"q": map[string]any{"type": "string"}},
			},
			Annotations: mcpspec.ToolAnnotation{ReadOnlyHint: &readOnly},
		},
		func(_ context.Context, req mcpspec.CallToolRequest) (*mcpspec.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			return mcpspec.NewToolResultText("looked up: " + asString(args["q"])), nil
		},
	)

	srv.AddTool(
		mcpspec.Tool{
			Name:        "mutate",
			Description: "Change something.",
			InputSchema: mcpspec.ToolInputSchema{Type: "object"},
		},
		func(_ context.Context, _ mcpspec.CallToolRequest) (*mcpspec.CallToolResult, error) {
			return mcpspec.NewToolResultError("nope"), nil
		},
	)

	return srv
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// inProcessManager returns a Manager wired to an in-process server under
// the name "fake", already connected.
func inProcessManager(t *testing.T) *Manager {
	t.Helper()
	srv := fakeServer(t)
	m := NewManager(zap.NewNop(), 5*time.Second, 5*time.Second)

	sc := &serverClient{
		name:   "fake",
		cfg:    config.MCPServerConfig{Enabled: true, Command: "in-process"},
		logger: zap.NewNop(),
		// In-process transport: no child process, so no *exec.Cmd.
		dial: func() (*mcpclient.Client, *exec.Cmd, error) {
			c, err := mcpclient.NewInProcessClient(srv)
			return c, nil, err
		},
	}
	m.servers["fake"] = sc

	if err := sc.connect(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestConnect_DiscoversToolsWithSchemaAndAnnotations(t *testing.T) {
	m := inProcessManager(t)

	tools := m.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}

	// Sorted by (server, name) → lookup before mutate.
	lookup, mutate := tools[0], tools[1]

	if lookup.Name != "lookup" || mutate.Name != "mutate" {
		t.Fatalf("unexpected order: %q, %q", lookup.Name, mutate.Name)
	}
	if lookup.Server != "fake" {
		t.Errorf("server: got %q want %q", lookup.Server, "fake")
	}
	if !lookup.ReadOnly {
		t.Error("lookup: readOnlyHint should carry through as ReadOnly=true")
	}
	// An absent hint must NOT be read as read-only.
	if mutate.ReadOnly {
		t.Error("mutate: no readOnlyHint should mean ReadOnly=false")
	}
	if lookup.Description != "Look something up." {
		t.Errorf("description: got %q", lookup.Description)
	}
	if lookup.InputSchema["type"] != "object" {
		t.Errorf("schema not decoded: %+v", lookup.InputSchema)
	}
}

func TestCallTool_SuccessAndToolError(t *testing.T) {
	m := inProcessManager(t)

	res, err := m.CallTool(context.Background(), "fake", "lookup", map[string]any{"q": "vLLM"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Error("lookup should not report IsError")
	}
	if !strings.Contains(res.Text, "looked up: vLLM") {
		t.Errorf("text: got %q", res.Text)
	}

	// A tool that ran and failed is IsError, NOT a Go error — the
	// distinction the bridge relies on to decide between a tool_result
	// and a dispatch failure.
	res, err = m.CallTool(context.Background(), "fake", "mutate", nil)
	if err != nil {
		t.Fatalf("tool-level failure must not surface as a Go error: %v", err)
	}
	if !res.IsError {
		t.Error("mutate should report IsError")
	}
}

func TestCallTool_UnknownServer(t *testing.T) {
	m := NewManager(zap.NewNop(), time.Second, time.Second)
	_, err := m.CallTool(context.Background(), "nope", "x", nil)
	if !errors.Is(err, ErrUnknownServer) {
		t.Errorf("got %v want ErrUnknownServer", err)
	}
}

// After a transport failure the connection is dropped, and the next call
// must transparently reconnect. Without this a server that died once
// would stay dead for the life of the process.
func TestCallTool_ReconnectsAfterTransportDrop(t *testing.T) {
	m := inProcessManager(t)
	sc := m.servers["fake"]

	sc.dropConnection()

	sc.mu.Lock()
	stillConnected := sc.inner != nil
	toolsRetained := len(sc.tools)
	sc.mu.Unlock()

	if stillConnected {
		t.Fatal("dropConnection should clear the client")
	}
	// Descriptors survive a drop on purpose: losing them would make the
	// tool vanish from the LLM's schema mid-session on a blip.
	if toolsRetained != 2 {
		t.Errorf("descriptors should survive a drop, got %d", toolsRetained)
	}

	res, err := m.CallTool(context.Background(), "fake", "lookup", map[string]any{"q": "again"})
	if err != nil {
		t.Fatalf("call after drop should reconnect: %v", err)
	}
	if !strings.Contains(res.Text, "again") {
		t.Errorf("text: got %q", res.Text)
	}
}

func TestConnect_SkipsDisabledAndSurvivesFailure(t *testing.T) {
	m := NewManager(zap.NewNop(), 2*time.Second, 2*time.Second)

	// A command that cannot exist. Connect must log and continue rather
	// than block or panic — MCP is additive and must never break boot.
	m.Connect(context.Background(), map[string]config.MCPServerConfig{
		"broken":  {Enabled: true, Command: "definitely-not-a-real-binary-xyz"},
		"skipped": {Enabled: false, Command: "also-fake"},
	})

	if got := m.Tools(); len(got) != 0 {
		t.Errorf("failed server should contribute no tools, got %+v", got)
	}
	// The failed server stays registered so the lazy reconnect can retry.
	names := m.ServerNames()
	if len(names) != 1 || names[0] != "broken" {
		t.Errorf("expected only the enabled server registered, got %v", names)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close on never-connected server: %v", err)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	m := inProcessManager(t)
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}

func TestEnvSlice_PreservesKeyCaseAndLayersOverProcessEnv(t *testing.T) {
	sc := &serverClient{
		cfg: config.MCPServerConfig{
			Env: map[string]string{"GITHUB_TOKEN": "ghp_x", "lower_ok": "1"},
		},
	}
	env := sc.envSlice()

	var sawUpper, sawLower bool
	for _, kv := range env {
		if kv == "GITHUB_TOKEN=ghp_x" {
			sawUpper = true
		}
		if kv == "lower_ok=1" {
			sawLower = true
		}
	}
	if !sawUpper {
		t.Error("uppercase env key must survive verbatim")
	}
	if !sawLower {
		t.Error("lowercase env key must survive verbatim")
	}
	// Process env is layered underneath so the child still sees PATH.
	if len(env) <= 2 {
		t.Errorf("expected process env to be included, got %d entries", len(env))
	}
}

func TestEnvSlice_NoConfigEnvReturnsProcessEnv(t *testing.T) {
	sc := &serverClient{cfg: config.MCPServerConfig{}}
	if len(sc.envSlice()) == 0 {
		t.Error("expected process environment")
	}
}

func TestCollapseContent(t *testing.T) {
	t.Run("joins text blocks", func(t *testing.T) {
		got := collapseContent([]mcpspec.Content{
			mcpspec.TextContent{Type: "text", Text: "one"},
			mcpspec.TextContent{Type: "text", Text: "two"},
		}, nil)
		if got != "one\ntwo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("marks non-text rather than dropping", func(t *testing.T) {
		got := collapseContent([]mcpspec.Content{
			mcpspec.ImageContent{Type: "image", Data: "x", MIMEType: "image/png"},
		}, nil)
		if !strings.Contains(got, "non-text content") {
			t.Errorf("expected a marker, got %q", got)
		}
	})

	t.Run("falls back to structured content", func(t *testing.T) {
		got := collapseContent(nil, map[string]any{"ok": true})
		if !strings.Contains(got, `"ok":true`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("empty stays empty", func(t *testing.T) {
		if got := collapseContent(nil, nil); got != "" {
			t.Errorf("got %q", got)
		}
	})
}

func TestTimeoutFor_PerServerOverridesDefault(t *testing.T) {
	m := NewManager(zap.NewNop(), 30*time.Second, 5*time.Second)

	if got := m.timeoutFor(config.MCPServerConfig{}); got != 30*time.Second {
		t.Errorf("zero timeout should use manager default, got %s", got)
	}
	if got := m.timeoutFor(config.MCPServerConfig{Timeout: time.Second}); got != time.Second {
		t.Errorf("per-server timeout should win, got %s", got)
	}
}

func TestNewManager_ZeroTimeoutsFallBackToDefaults(t *testing.T) {
	m := NewManager(nil, 0, 0)
	if m.defaultTimeout != DefaultRequestTimeout {
		t.Errorf("request: got %s want %s", m.defaultTimeout, DefaultRequestTimeout)
	}
	if m.connectTimeout != DefaultConnectTimeout {
		t.Errorf("connect: got %s want %s", m.connectTimeout, DefaultConnectTimeout)
	}
	if m.logger == nil {
		t.Error("nil logger should be replaced with a no-op")
	}
}

// The handshake budget must stay well under the call budget: Connect
// blocks engine startup, so borrowing engine.tool_timeout (120s) for it
// is what turned one bad server into a multi-minute boot stall.
func TestConnectTimeoutFor_SeparateFromCallBudget(t *testing.T) {
	m := NewManager(zap.NewNop(), 120*time.Second, 20*time.Second)

	if got := m.timeoutFor(config.MCPServerConfig{}); got != 120*time.Second {
		t.Errorf("call budget: got %s want 120s", got)
	}
	if got := m.connectTimeoutFor(config.MCPServerConfig{}); got != 20*time.Second {
		t.Errorf("handshake budget: got %s want 20s", got)
	}
	if m.connectTimeout >= m.defaultTimeout {
		t.Error("handshake budget should be tighter than the call budget by default")
	}

	// An explicit per-server timeout is the operator saying "this one is
	// slow, wait for it" — it must raise BOTH bounds, not just calls.
	slow := config.MCPServerConfig{Timeout: 90 * time.Second}
	if got := m.connectTimeoutFor(slow); got != 90*time.Second {
		t.Errorf("explicit timeout should govern the handshake, got %s", got)
	}
	if got := m.timeoutFor(slow); got != 90*time.Second {
		t.Errorf("explicit timeout should govern calls, got %s", got)
	}
}

// Regression guard for the cascading-drop race. Tools dispatch in
// parallel, so two goroutines can hold the same client; when the first
// fails and reconnects, the second's late failure must not close the
// replacement.
func TestDropIfCurrent_LeavesReplacementAlone(t *testing.T) {
	m := inProcessManager(t)
	sc := m.servers["fake"]

	sc.mu.Lock()
	first := sc.inner
	sc.mu.Unlock()
	if first == nil {
		t.Fatal("expected a connected client")
	}

	// Simulate goroutine A: drop and re-establish.
	sc.dropConnection()
	if err := sc.connect(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	sc.mu.Lock()
	second := sc.inner
	sc.mu.Unlock()
	if second == nil {
		t.Fatal("expected a reconnected client")
	}
	if second == first {
		t.Fatal("reconnect returned the same client; test cannot detect the race")
	}

	// Now goroutine B reports its stale failure against the OLD client.
	sc.dropIfCurrent(first)

	sc.mu.Lock()
	got := sc.inner
	sc.mu.Unlock()
	if got != second {
		t.Error("a stale drop closed the healthy replacement connection")
	}

	// And the surviving connection is genuinely still usable.
	if _, err := m.CallTool(context.Background(), "fake", "lookup", map[string]any{"q": "ok"}); err != nil {
		t.Errorf("replacement should still work: %v", err)
	}
}

// dropIfCurrent on the live client does drop it — the guard must not
// become a no-op that defeats reconnection entirely.
func TestDropIfCurrent_DropsTheLiveClient(t *testing.T) {
	m := inProcessManager(t)
	sc := m.servers["fake"]

	sc.mu.Lock()
	live := sc.inner
	sc.mu.Unlock()

	sc.dropIfCurrent(live)

	sc.mu.Lock()
	got := sc.inner
	sc.mu.Unlock()
	if got != nil {
		t.Error("dropIfCurrent should clear the client it was given")
	}
}

func TestDropIfCurrent_NilIsNoop(t *testing.T) {
	m := inProcessManager(t)
	sc := m.servers["fake"]

	sc.dropIfCurrent(nil)

	sc.mu.Lock()
	got := sc.inner
	sc.mu.Unlock()
	if got == nil {
		t.Error("nil should be a no-op, not a drop")
	}
}

// Parallel tool dispatch is the normal case in this engine, so the
// connection state has to survive concurrent calls against one server.
// Run under -race; this is what turns the dropIfCurrent guard from an
// argument into a check.
func TestCallTool_ConcurrentCallsAreSafe(t *testing.T) {
	m := inProcessManager(t)

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Half the callers hit a tool that succeeds, half hit the one
			// that reports a tool-level error, so both result paths run
			// concurrently.
			name := "lookup"
			if n%2 == 1 {
				name = "mutate"
			}
			if _, err := m.CallTool(context.Background(), "fake", name, map[string]any{"q": "x"}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}
}
