//go:build integration

package mcptool_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/mcp"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/tools/mcptool"
)

// TestPipeline_ConfigToFreelancerPool walks the entire feature the way
// cmd/server/main.go does: parse a real YAML config, connect the servers
// it names, adapt the discovered tools, then build the pool a freelancer
// sub-agent would actually run with — and assert the MCP tool survived
// every filter on the way.
//
// This is the test that would have caught the two design traps found
// while building this: viper lowercasing env keys, and AllowedTools
// silently dropping MCP tools because their names cannot be listed
// statically.
//
//	go test -tags=integration ./internal/tools/mcptool/ -v
func TestPipeline_ConfigToFreelancerPool(t *testing.T) {
	fixture := buildFixture(t)

	// 1. Real config parse, exactly as the engine does it.
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := "" +
		"tools:\n" +
		"  mcp_servers:\n" +
		"    fixture:\n" +
		"      enabled: true\n" +
		"      command: " + fixture + "\n" +
		"      timeout: \"30s\"\n" +
		"      env:\n" +
		"        GITHUB_TOKEN: ghp_pipeline\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.SanitizeMCPServers(zap.NewNop())

	if got := cfg.EnabledMCPServers(); len(got) != 1 || got[0] != "fixture" {
		t.Fatalf("expected the fixture server to survive sanitize, got %v", got)
	}

	// 2. Connect + adapt, as main.go does.
	mgr := mcp.NewManager(zap.NewNop(), 30*time.Second, 30*time.Second)
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.Connect(context.Background(), cfg.Tools.MCPServers)

	mcpTools := mcptool.NewTools(mgr)
	if len(mcpTools) != 1 {
		t.Fatalf("expected 1 adapted tool, got %d", len(mcpTools))
	}
	want := "mcp__fixture__echo_env"
	if mcpTools[0].Name() != want {
		t.Fatalf("name: got %q want %q", mcpTools[0].Name(), want)
	}

	// 3. Build the pool a freelancer actually gets, using the real
	// builtin definition — whitelist, agent-type filter and all.
	def := builtin.Freelancer
	pool := common.BuildToolPool(
		emptyRegistry(t),
		def.MaybeAugmentForSubAgent(),
		def.AgentType,
		def.EffectiveTier() == definition.TierSubAgent,
		common.WithMCPTools(mcpTools),
	)

	if pool.Get(want) == nil {
		t.Fatalf("MCP tool did not survive the freelancer pool filters; pool = %v", pool.Names())
	}

	// 4. And it is genuinely callable through the engine interface.
	res, err := pool.Get(want).Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "token=ghp_pipeline") {
		t.Errorf("env case lost somewhere in the pipeline: %q", res.Content)
	}
}

// emptyRegistry returns a valid registry with no built-ins. The pipeline
// under test only needs the MCP tools present; built-in assembly is
// covered by the pool tests.
func emptyRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	return tool.NewRegistry()
}

func buildFixture(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fakeserver")
	cmd := exec.Command("go", "build", "-o", out, "../../mcp/testdata/fakeserver/main.go")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, combined)
	}
	return out
}
