//go:build integration

package mcp

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

// These are the regression guards for the two unbounded-blocking paths
// found in review. mcp-go's stdio Close() ends in cmd.Wait() with no
// timeout and no kill, so an uncooperative child blocked engine startup
// (via the connect-failure path) and engine shutdown (via Manager.Close)
// indefinitely.
//
// Behind the integration tag because they spawn real processes and spend
// real wall-clock time on timeouts.
//
//	go test -tags=integration ./internal/mcp/ -run Lifecycle -v
//
// `sleep` is the worst realistic case: it accepts the spawn, never speaks
// JSON-RPC, and ignores stdin close. Before the fix this blocked forever
// in mcp-go's Close() -> cmd.Wait().
func TestLifecycle_UncooperativeServerBoundsConnectAndClose(t *testing.T) {
	m := NewManager(zap.NewNop(), 60*time.Second, 2*time.Second)

	connectStart := time.Now()
	m.Connect(context.Background(), map[string]config.MCPServerConfig{
		"mute": {Enabled: true, Command: "sleep", Args: []string{"300"}},
	})
	connectElapsed := time.Since(connectStart)
	t.Logf("Connect returned after %s", connectElapsed)

	// 2s handshake + up to 5s close grace, nowhere near the 60s call budget.
	if connectElapsed > 20*time.Second {
		t.Fatalf("Connect took %s — startup is still unbounded", connectElapsed)
	}
	if got := m.Tools(); len(got) != 0 {
		t.Errorf("a mute server must advertise nothing, got %+v", got)
	}

	closeStart := time.Now()
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	closeElapsed := time.Since(closeStart)
	t.Logf("Close returned after %s", closeElapsed)
	if closeElapsed > 15*time.Second {
		t.Fatalf("Close took %s — shutdown is still unbounded", closeElapsed)
	}
}

// Several stubborn servers must cost ONE grace period, not one each.
func TestLifecycle_CloseIsParallelAcrossStubbornServers(t *testing.T) {
	m := NewManager(zap.NewNop(), 60*time.Second, time.Second)
	m.Connect(context.Background(), map[string]config.MCPServerConfig{
		"a": {Enabled: true, Command: "sleep", Args: []string{"300"}},
		"b": {Enabled: true, Command: "sleep", Args: []string{"300"}},
		"c": {Enabled: true, Command: "sleep", Args: []string{"300"}},
	})

	start := time.Now()
	_ = m.Close()
	elapsed := time.Since(start)
	t.Logf("Close of 3 stubborn servers took %s", elapsed)

	if elapsed > 2*closeGracePeriod {
		t.Errorf("Close took %s — looks sequential, not parallel", elapsed)
	}
}
