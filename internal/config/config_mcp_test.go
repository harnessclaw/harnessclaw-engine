package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCfg drops raw YAML into a temp file and returns its path.
func writeCfg(t *testing.T, raw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMCPServers_YAMLRoundTrip(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
tools:
  mcp_servers:
    filesystem:
      enabled: true
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "~/Documents"]
      timeout: "30s"
      env:
        ACME_TOKEN: shh
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	srv, ok := cfg.Tools.MCPServers["filesystem"]
	if !ok {
		t.Fatalf("filesystem entry missing; got keys %v", keysOf(cfg.Tools.MCPServers))
	}
	if !srv.Enabled {
		t.Error("enabled: got false want true")
	}
	if srv.Command != "npx" {
		t.Errorf("command: got %q want %q", srv.Command, "npx")
	}
	wantArgs := []string{"-y", "@modelcontextprotocol/server-filesystem", "~/Documents"}
	if len(srv.Args) != len(wantArgs) {
		t.Fatalf("args: got %v want %v", srv.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if srv.Args[i] != a {
			t.Errorf("args[%d]: got %q want %q", i, srv.Args[i], a)
		}
	}
	if srv.Timeout != 30*time.Second {
		t.Errorf("timeout: got %s want 30s", srv.Timeout)
	}
	if srv.Env["ACME_TOKEN"] != "shh" {
		t.Errorf("env[ACME_TOKEN]: got %q want %q", srv.Env["ACME_TOKEN"], "shh")
	}
}

// Absent key must not error and must not fabricate entries — the engine
// has to boot fine for the overwhelming majority of users who configure
// no MCP servers at all.
func TestMCPServers_AbsentIsEmpty(t *testing.T) {
	cfg, err := Load(writeCfg(t, "tools:\n  bash:\n    enabled: true\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Tools.MCPServers) != 0 {
		t.Errorf("expected no servers; got %v", keysOf(cfg.Tools.MCPServers))
	}
}

// Load uses viper's "::" key delimiter precisely so YAML keys carrying
// dots stay opaque. Server names are user-chosen, so this pins that a
// dotted name survives as ONE key rather than being split into a nested
// path (the bug the delimiter choice exists to prevent).
func TestMCPServers_DottedNameStaysOneKey(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
tools:
  mcp_servers:
    acme.files:
      enabled: true
      command: acme-mcp
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Tools.MCPServers["acme.files"]; !ok {
		t.Errorf("dotted name was split; got keys %v", keysOf(cfg.Tools.MCPServers))
	}
	if len(cfg.Tools.MCPServers) != 1 {
		t.Errorf("expected exactly 1 server; got %v", keysOf(cfg.Tools.MCPServers))
	}
}

func TestMCPServerConfig_EffectiveTransport(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", MCPTransportStdio},
		{"stdio", MCPTransportStdio},
		{"sse", "sse"},
	} {
		if got := (MCPServerConfig{Transport: tc.in}).EffectiveTransport(); got != tc.want {
			t.Errorf("Transport=%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeMCPServers_DropsMalformed(t *testing.T) {
	tests := []struct {
		name string
		srv  MCPServerConfig
		keep bool
	}{
		{"valid stdio", MCPServerConfig{Enabled: true, Command: "acme-mcp"}, true},
		{"valid disabled", MCPServerConfig{Command: "acme-mcp"}, true},
		{"explicit stdio", MCPServerConfig{Command: "acme-mcp", Transport: "stdio"}, true},
		{"no command", MCPServerConfig{Enabled: true}, false},
		{"blank command", MCPServerConfig{Enabled: true, Command: "   "}, false},
		{"sse not implemented", MCPServerConfig{Command: "x", Transport: "sse"}, false},
		{"negative timeout", MCPServerConfig{Command: "x", Timeout: -time.Second}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Tools.MCPServers = map[string]MCPServerConfig{"srv": tc.srv}
			cfg.SanitizeMCPServers(nil)

			_, kept := cfg.Tools.MCPServers["srv"]
			if kept != tc.keep {
				t.Errorf("kept = %v, want %v", kept, tc.keep)
			}
		})
	}
}

// A disabled entry is shape-checked too, so a typo surfaces at startup
// rather than the first time someone flips enabled: true.
func TestSanitizeMCPServers_ChecksDisabledEntries(t *testing.T) {
	cfg := &Config{}
	cfg.Tools.MCPServers = map[string]MCPServerConfig{
		"broken": {Enabled: false, Command: ""},
	}
	cfg.SanitizeMCPServers(nil)

	if _, kept := cfg.Tools.MCPServers["broken"]; kept {
		t.Error("disabled entry with no command should still be dropped")
	}
}

func TestSanitizeMCPServers_DropsEmptyName(t *testing.T) {
	cfg := &Config{}
	cfg.Tools.MCPServers = map[string]MCPServerConfig{"": {Enabled: true, Command: "x"}}
	cfg.SanitizeMCPServers(nil)

	if len(cfg.Tools.MCPServers) != 0 {
		t.Errorf("empty name should be dropped; got %v", keysOf(cfg.Tools.MCPServers))
	}
}

// Nil map and nil logger are both valid inputs — sanitize runs
// unconditionally at startup, before anyone knows whether MCP is in use.
func TestSanitizeMCPServers_NilMapIsNoop(t *testing.T) {
	cfg := &Config{}
	cfg.SanitizeMCPServers(nil)
	if len(cfg.Tools.MCPServers) != 0 {
		t.Error("nil map should stay empty")
	}
}

func TestEnabledMCPServers_SortedAndFiltered(t *testing.T) {
	cfg := &Config{}
	cfg.Tools.MCPServers = map[string]MCPServerConfig{
		"zeta":  {Enabled: true, Command: "z"},
		"alpha": {Enabled: true, Command: "a"},
		"mid":   {Enabled: false, Command: "m"},
	}

	got := cfg.EnabledMCPServers()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func keysOf(m map[string]MCPServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Env keys must survive with exact case. viper lowercases every key it
// stores, and env vars are case-sensitive on Unix — an MCP server handed
// `acme_token` instead of `ACME_TOKEN` starts fine and then fails to
// authenticate, so this is the regression guard for applyRawMCPEnv.
func TestMCPServers_EnvKeyCasePreserved(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
tools:
  mcp_servers:
    github:
      enabled: true
      command: mcp-github
      env:
        GITHUB_TOKEN: ghp_example
        lower_case_kept: yes-really
        Mixed_Case: v2
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.Tools.MCPServers["github"].Env

	for k, want := range map[string]string{
		"GITHUB_TOKEN":    "ghp_example",
		"lower_case_kept": "yes-really",
		"Mixed_Case":      "v2",
	} {
		if got := env[k]; got != want {
			t.Errorf("env[%q]: got %q want %q (all keys: %v)", k, got, want, envKeys(env))
		}
	}
	if len(env) != 3 {
		t.Errorf("expected 3 env keys, got %v", envKeys(env))
	}
}

// Server names ARE lowercased by viper. That is acceptable — the MCP
// ecosystem names servers in lowercase by convention, and the name only
// feeds tool-name prefixes — but it is surprising enough to pin so a
// future reader knows it is intentional rather than an oversight.
func TestMCPServers_ServerNameIsLowercased(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
tools:
  mcp_servers:
    MyServer:
      enabled: true
      command: x
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Tools.MCPServers["myserver"]; !ok {
		t.Errorf("expected lowercased key %q; got %v", "myserver", keysOf(cfg.Tools.MCPServers))
	}
}

func envKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
