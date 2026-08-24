//go:build integration

package mcp

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

// TestStdioTransport_EndToEnd spawns a real child process and drives the
// full production path: stdioDialer → initialize → tools/list →
// tools/call → Close.
//
// Behind the integration tag because it compiles a fixture binary, which
// is far too slow for `make test`. Run with:
//
//	go test -tags=integration ./internal/mcp/ -v
//
// The in-process tests in mcp_test.go cover the same logic through an
// injected dialer; this one exists to prove the stdio dialer itself
// works, which nothing else can.
func TestStdioTransport_EndToEnd(t *testing.T) {
	bin := buildFixture(t)

	m := NewManager(zap.NewNop(), 30*time.Second, 30*time.Second)
	t.Cleanup(func() { _ = m.Close() })

	m.Connect(context.Background(), map[string]config.MCPServerConfig{
		"fixture": {
			Enabled: true,
			Command: bin,
			// Uppercase on purpose: this is the regression guard for the
			// viper key-lowercasing trap. The fixture reads
			// os.Getenv("GITHUB_TOKEN"), so a mangled key returns empty.
			Env: map[string]string{"GITHUB_TOKEN": "ghp_end_to_end"},
		},
	})

	tools := m.Tools()
	if len(tools) != 1 || tools[0].Name != "echo_env" {
		t.Fatalf("expected the fixture's one tool, got %+v", tools)
	}
	if tools[0].Server != "fixture" {
		t.Errorf("server: got %q", tools[0].Server)
	}

	res, err := m.CallTool(context.Background(), "fixture", "echo_env", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %q", res.Text)
	}
	if !strings.Contains(res.Text, "token=ghp_end_to_end") {
		t.Errorf("env var did not survive with exact case: got %q", res.Text)
	}
}

// buildFixture compiles testdata/fakeserver into the test's temp dir and
// returns the binary path.
func buildFixture(t *testing.T) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), "fakeserver")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fakeserver/main.go")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, combined)
	}
	return out
}
