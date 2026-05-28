package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
)

const (
	testCDPEndpoint = "ws://127.0.0.1:9222/devtools/page/1"
	testCDPSession  = "harnessclaw-browser-d60e733519db9673"
)

type fakeRunner struct {
	args []string
	out  []byte
	err  error
}

func (r *fakeRunner) Run(_ context.Context, args []string) ([]byte, error) {
	r.args = append([]string(nil), args...)
	return r.out, r.err
}

func TestSessionCreateTool_ClientRoutedAndDangerous(t *testing.T) {
	tl := NewSessionCreateTool(config.BrowserAgentConfig{
		Enabled:           true,
		DefaultVisibility: "hidden",
		MaxSteps:          30,
		BlockedDomains:    []string{"blocked.example"},
	})

	if tl.Name() != "browser_session_create" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	if !tl.IsEnabled() {
		t.Fatal("tool should be enabled from config")
	}
	if routed, ok := any(tl).(tool.ClientRoutedTool); !ok || !routed.IsClientRouted() {
		t.Fatal("browser_session_create must be client-routed")
	}
	if got := tool.EffectiveSafetyLevel(tl); got != tool.SafetyDangerous {
		t.Fatalf("safety = %s, want %s", got, tool.SafetyDangerous)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"start_url":"https://example.com","visibility":"visible"}`)); err != nil {
		t.Fatalf("ValidateInput valid: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"start_url":"https://blocked.example","visibility":"hidden"}`)); err == nil {
		t.Fatal("blocked domain should be rejected")
	}
	props := tl.InputSchema()["properties"].(map[string]any)
	startURL := props["start_url"].(map[string]any)
	desc := startURL["description"].(string)
	if !strings.Contains(desc, "browser_navigate") {
		t.Fatalf("start_url schema description should direct navigation through browser_navigate: %q", desc)
	}
	if strings.Contains(desc, "直接加载") {
		t.Fatalf("start_url schema description should not imply direct client loading: %q", desc)
	}
}

func TestNavigateTool_ExecutesAgentBrowserWithCDPAndJSON(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":{"url":"https://example.com"}}`)}
	tl := NewNavigateTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}

	want := testCDPArgs("open", "https://example.com")
	assertArgs(t, runner.args, want)
	if !strings.Contains(res.Content, "example.com") {
		t.Fatalf("content = %q, want url", res.Content)
	}
}

func TestSnapshotTool_DefaultsToInteractiveSnapshot(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":{"snapshot":"@e1 [button] \"Submit\""}}`)}
	tl := NewSnapshotTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}

	want := testCDPArgs("snapshot", "-i")
	assertArgs(t, runner.args, want)
	if !strings.Contains(res.Content, "@e1") {
		t.Fatalf("content = %q, want snapshot text", res.Content)
	}
}

func TestExtractTool_DefaultsToBodyText(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"visible page text"}`)}
	tl := NewExtractTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}

	want := testCDPArgs("get", "text", "body")
	assertArgs(t, runner.args, want)
	if res.Content != "visible page text" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBrowserOperation_CLIErrorBecomesToolError(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":false,"error":"navigation failed"}`)}
	tl := NewNavigateTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "navigation failed") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestWithCDP_UsesStableIsolatedAgentBrowserSession(t *testing.T) {
	got := withCDP(testCDPEndpoint, "snapshot", "-i")
	want := testCDPArgs("snapshot", "-i")

	assertArgs(t, got, want)
}

func TestInteractionTools_BuildAgentBrowserCommands(t *testing.T) {
	cases := []struct {
		name string
		tool func(config.BrowserAgentConfig, Runner) tool.Tool
		in   string
		want []string
	}{
		{
			name: "click",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewClickTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","ref":"@e1"}`,
			want: testCDPArgs("click", "@e1"),
		},
		{
			name: "fill",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewFillTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","ref":"@e2","text":"hello"}`,
			want: testCDPArgs("fill", "@e2", "hello"),
		},
		{
			name: "press",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewPressTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","key":"Enter"}`,
			want: testCDPArgs("press", "Enter"),
		},
		{
			name: "scroll",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewScrollTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","direction":"down","amount":500}`,
			want: testCDPArgs("scroll", "down", "500"),
		},
		{
			name: "back",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewBackTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1"}`,
			want: testCDPArgs("back"),
		},
		{
			name: "wait-load",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewWaitTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","load_state":"networkidle"}`,
			want: testCDPArgs("wait", "--load", "networkidle"),
		},
		{
			name: "tabs-list",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewTabsTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","action":"list"}`,
			want: testCDPArgs("tab"),
		},
		{
			name: "screenshot",
			tool: func(cfg config.BrowserAgentConfig, r Runner) tool.Tool {
				return NewScreenshotTool(cfg, r)
			},
			in:   `{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","path":"/tmp/page.png","annotate":true}`,
			want: testCDPArgs("screenshot", "--annotate", "/tmp/page.png"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
			tl := tc.tool(testBrowserConfig(), runner)
			res, err := tl.Execute(context.Background(), json.RawMessage(tc.in))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.IsError {
				t.Fatalf("Execute returned error result: %+v", res)
			}
			assertArgs(t, runner.args, tc.want)
		})
	}
}

func TestAskHumanTool_ClientRouted(t *testing.T) {
	tl := NewAskHumanTool(config.BrowserAgentConfig{Enabled: true})
	if tl.Name() != "browser_ask_human" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	if routed, ok := any(tl).(tool.ClientRoutedTool); !ok || !routed.IsClientRouted() {
		t.Fatal("browser_ask_human must be client-routed")
	}
	if err := tl.ValidateInput(json.RawMessage(`{"session_id":"s1","message":"请完成验证码"}`)); err != nil {
		t.Fatalf("ValidateInput valid: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"session_id":"s1","message":"   "}`)); err == nil {
		t.Fatal("blank message should be rejected")
	}
}

func TestCommandRunner_UsesPathNodeForNodeShebangScripts(t *testing.T) {
	tmp := t.TempDir()
	nodePath := filepath.Join(tmp, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	scriptPath := filepath.Join(tmp, "agent-browser")
	if err := os.WriteFile(scriptPath, []byte("#!/opt/homebrew/opt/node/bin/node\nconsole.log('ok')\n"), 0o755); err != nil {
		t.Fatalf("write fake agent-browser: %v", err)
	}
	t.Setenv("PATH", tmp)

	runner := &CommandRunner{binaryPath: scriptPath}
	cmd := runner.command(context.Background(), []string{"--json", "snapshot"})

	if cmd.Path != nodePath {
		t.Fatalf("cmd.Path = %q, want PATH node %q", cmd.Path, nodePath)
	}
	wantArgs := []string{nodePath, scriptPath, "--json", "snapshot"}
	assertArgs(t, cmd.Args, wantArgs)
}

func testBrowserConfig() config.BrowserAgentConfig {
	return config.BrowserAgentConfig{
		Enabled:    true,
		BinaryPath: "agent-browser",
		CLITimeout: 25 * time.Second,
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args len = %d, want %d: got %v want %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q; got %v", i, got[i], want[i], got)
		}
	}
}

func testCDPArgs(command ...string) []string {
	args := []string{"--session", testCDPSession, "--cdp", testCDPEndpoint, "--json"}
	args = append(args, command...)
	return args
}
