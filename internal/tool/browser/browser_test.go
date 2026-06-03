package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
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
	if _, ok := props["partition"]; ok {
		t.Fatal("browser_session_create schema should not expose partition")
	}
	if _, ok := props["task_id"]; ok {
		t.Fatal("browser_session_create schema should not expose task_id")
	}
	startURL := props["start_url"].(map[string]any)
	desc := startURL["description"].(string)
	if !strings.Contains(desc, "agent_browser_command") {
		t.Fatalf("start_url schema description should direct navigation through agent_browser_command: %q", desc)
	}
	if strings.Contains(desc, "直接加载") {
		t.Fatalf("start_url schema description should not imply direct client loading: %q", desc)
	}
	if !strings.Contains(tl.Description(), "全局持久 profile") {
		t.Fatalf("description should document global persistent profile:\n%s", tl.Description())
	}
}

func TestAgentBrowserCommandTool_ExecutesWithCDPAndJSON(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":{"snapshot":"@e1 [button] \"Submit\""}}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	res, err := tl.Execute(browserTaskContext("browser_123"), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","args":["snapshot","-i"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}

	want := []string{
		"--session", "harnessclaw-browser-browser_123",
		"--cdp", testCDPEndpoint,
		"--json",
		"snapshot",
		"-i",
	}
	assertArgs(t, runner.args, want)
	if !strings.Contains(res.Content, "@e1") {
		t.Fatalf("content = %q, want snapshot text", res.Content)
	}
}

func TestAgentBrowserCommandTool_DoesNotExposeSessionID(t *testing.T) {
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), &fakeRunner{})
	props := tl.InputSchema()["properties"].(map[string]any)
	if _, ok := props["session_id"]; ok {
		t.Fatal("agent_browser_command schema must not expose session_id")
	}
}

func TestAgentBrowserCommandTool_UsesBoundCDPEndpointWhenModelOmitsIt(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)
	binding := NewTaskBinding("browser_123")
	binding.UpdateCDPEndpoint(testCDPEndpoint)
	ctx := WithTaskBinding(browserTaskContext("browser_123"), binding)

	res, err := tl.Execute(ctx, json.RawMessage(`{"args":["snapshot"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}

	want := []string{
		"--session", "harnessclaw-browser-browser_123",
		"--cdp", testCDPEndpoint,
		"--json",
		"snapshot",
	}
	assertArgs(t, runner.args, want)
}

func TestAgentBrowserCommandTool_DoesNotRequireModelCDPEndpointInSchema(t *testing.T) {
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), &fakeRunner{})
	for _, required := range tl.InputSchema()["required"].([]string) {
		if required == "cdp_endpoint" {
			t.Fatal("agent_browser_command schema must not require model-supplied cdp_endpoint when a task binding exists")
		}
	}
}

func TestAgentBrowserCommandTool_RejectsMismatchedModelSessionID(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	res, err := tl.Execute(browserTaskContext("browser_123"), json.RawMessage(`{
		"session_id":"foreign",
		"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1",
		"args":["snapshot"]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mismatched session_id should be rejected: %+v", res)
	}
	if len(runner.args) != 0 {
		t.Fatalf("runner should not execute on mismatched session_id, got %v", runner.args)
	}
}

func TestAgentBrowserCommandTool_RejectsMismatchedBoundCDPEndpoint(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)
	binding := NewTaskBinding("browser_123")
	binding.UpdateCDPEndpoint(testCDPEndpoint)
	ctx := WithTaskBinding(browserTaskContext("browser_123"), binding)

	res, err := tl.Execute(ctx, json.RawMessage(`{
		"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/foreign",
		"args":["snapshot"]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mismatched cdp_endpoint should be rejected: %+v", res)
	}
	if len(runner.args) != 0 {
		t.Fatalf("runner should not execute on mismatched cdp_endpoint, got %v", runner.args)
	}
}

func TestTaskBinding_UpdatesFromBrowserSessionToolResults(t *testing.T) {
	binding := NewTaskBinding("browser_123")
	msg := typesMessageWithTool("browser_session_state")
	results := []types.ToolResult{{
		Content: `{"active_tab":{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1"}}`,
	}}

	UpdateTaskBindingFromResults(msg, results, binding)

	if got := binding.CDPEndpoint(); got != testCDPEndpoint {
		t.Fatalf("CDPEndpoint = %q, want %q", got, testCDPEndpoint)
	}
}

func TestBrowserSkillReferenceTool_ReadsWhitelistedReference(t *testing.T) {
	root := seedSkillRoot(t)
	tl := NewSkillReferenceToolForTest(testBrowserConfig(), root)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"path":"references/session-management.md"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("reference read failed: %+v", res)
	}
	if !strings.Contains(res.Content, "SESSION REFERENCE") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBrowserSkillReferenceTool_RejectsUnsafePaths(t *testing.T) {
	root := seedSkillRoot(t)
	tl := NewSkillReferenceToolForTest(testBrowserConfig(), root)

	for _, raw := range []string{
		`{"path":"/tmp/secret.md"}`,
		`{"path":"../SKILL.md"}`,
		`{"path":"SKILL.md"}`,
		`{"path":"references/../SKILL.md"}`,
		`{"path":"references/not-md.txt"}`,
	} {
		if err := tl.ValidateInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected unsafe path rejection for %s", raw)
		}
	}
}

func TestBrowserAgentFinalResultTool_SubmitsContextTaskID(t *testing.T) {
	tl := NewFinalResultTool()
	ctx := browserTaskContext("browser_123")

	res, err := tl.Execute(ctx, json.RawMessage(`{"content":"done","source":"browser","evidence":["title"],"notes":"ok"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("final result should submit through context task id: %s", res.Content)
	}
	if accepted, _ := res.Metadata[submittool.MetadataKeyAccepted].(bool); !accepted {
		t.Fatalf("submission not accepted: %+v", res.Metadata)
	}
	if taskID, _ := res.Metadata["task_id"].(string); taskID != "browser_123" {
		t.Fatalf("task_id = %q, want browser_123", taskID)
	}
	result := res.Metadata["result"].(map[string]any)
	if result["source"] != "browser" || result["notes"] != "ok" {
		t.Fatalf("result metadata = %#v", result)
	}
}

func TestBrowserAgentFinalResultTool_DoesNotExposeTaskID(t *testing.T) {
	tl := NewFinalResultTool()
	props := tl.InputSchema()["properties"].(map[string]any)
	if _, ok := props["task_id"]; ok {
		t.Fatal("browser_agent_final_result schema must not expose task_id")
	}
}

func TestCleanupHelperSession_DisablesBoundSessionStream(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}

	res, err := CleanupHelperSession(context.Background(), testBrowserConfig(), runner, "browser_123")
	if err != nil {
		t.Fatalf("CleanupHelperSession: %v", err)
	}
	if res.IsError {
		t.Fatalf("cleanup should be best-effort success in this test: %+v", res)
	}
	want := []string{
		"--session", "harnessclaw-browser-browser_123",
		"--json",
		"stream",
		"disable",
	}
	assertArgs(t, runner.args, want)
}

func TestAgentBrowserCommandTool_RejectsInvalidCDP(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	err := tl.ValidateInput(json.RawMessage(`{"cdp_endpoint":"not-a-websocket","args":["snapshot"]}`))
	if err == nil {
		t.Fatal("invalid CDP endpoint should be rejected")
	}
	if !strings.Contains(err.Error(), "cdp_endpoint") {
		t.Fatalf("error = %v, want cdp_endpoint validation", err)
	}
}

func TestAgentBrowserCommandTool_RejectsEmptyArgs(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	err := tl.ValidateInput(json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","args":[]}`))
	if err == nil {
		t.Fatal("empty args should be rejected")
	}
	if !strings.Contains(err.Error(), "args") {
		t.Fatalf("error = %v, want args validation", err)
	}
}

func TestAgentBrowserCommandTool_RejectsHarnessOwnedFlags(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":true,"data":"ok"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	for _, forbidden := range []string{"--session", "--cdp", "--json"} {
		err := tl.ValidateInput(json.RawMessage(fmt.Sprintf(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","args":["snapshot","%s","value"]}`, forbidden)))
		if err == nil {
			t.Fatalf("args containing %q should be rejected", forbidden)
		}
		if !strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "harness") {
			t.Fatalf("error = %v, want harness-owned flag rejection for %q", err, forbidden)
		}
	}
}

func TestAgentBrowserCommandTool_CLIErrorBecomesToolError(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"success":false,"error":"command failed"}`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","args":["snapshot"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result, got %+v", res)
	}
	if !strings.Contains(res.Content, "command failed") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestAgentBrowserCommandTool_RawOutputFallback(t *testing.T) {
	runner := &fakeRunner{out: []byte(`not json output`)}
	tl := NewAgentBrowserCommandTool(testBrowserConfig(), runner)

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"cdp_endpoint":"ws://127.0.0.1:9222/devtools/page/1","args":["snapshot"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}
	if res.Content != "not json output" {
		t.Fatalf("content = %q, want raw output", res.Content)
	}
}

func TestNewTools_RegistersCommandGatewayNotOldWrappers(t *testing.T) {
	tools := NewTools(testBrowserConfig())

	var foundCommand bool
	var foundReference bool
	var foundFinal bool
	for _, tl := range tools {
		if tl.Name() == "agent_browser_command" {
			foundCommand = true
		}
		if tl.Name() == "browser_skill_reference" {
			foundReference = true
		}
		if tl.Name() == "browser_agent_final_result" {
			foundFinal = true
		}
		for _, forbidden := range []string{
			"browser_navigate",
			"browser_snapshot",
			"browser_extract",
			"browser_click",
			"browser_fill",
			"browser_press",
			"browser_scroll",
			"browser_back",
			"browser_wait",
			"browser_tabs",
			"browser_screenshot",
		} {
			if tl.Name() == forbidden {
				t.Fatalf("NewTools should not register old wrapper %q", forbidden)
			}
		}
	}
	if !foundCommand {
		t.Fatal("NewTools should register agent_browser_command")
	}
	if !foundReference {
		t.Fatal("NewTools should register browser_skill_reference")
	}
	if !foundFinal {
		t.Fatal("NewTools should register browser_agent_final_result")
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

func TestSessionStateTool_ClientRouted(t *testing.T) {
	tl := NewSessionStateTool(config.BrowserAgentConfig{Enabled: true})
	if tl.Name() != "browser_session_state" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	if routed, ok := any(tl).(tool.ClientRoutedTool); !ok || !routed.IsClientRouted() {
		t.Fatal("browser_session_state must be client-routed")
	}
	if err := tl.ValidateInput(json.RawMessage(`{"session_id":"s1"}`)); err != nil {
		t.Fatalf("ValidateInput valid: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"session_id":"   "}`)); err == nil {
		t.Fatal("blank session_id should be rejected")
	}
	props := tl.InputSchema()["properties"].(map[string]any)
	sessionID := props["session_id"].(map[string]any)
	if sessionID["minLength"] != 1 {
		t.Fatalf("session_id schema = %#v, want minLength 1", sessionID)
	}
}

func TestCommandRunner_ExecutesConfiguredBinaryDirectly(t *testing.T) {
	runner := &CommandRunner{binaryPath: "/opt/harnessclaw/bin/agent-browser-darwin-arm64"}
	cmd := runner.command(context.Background(), []string{"--json", "snapshot"})

	if cmd.Path != runner.binaryPath {
		t.Fatalf("cmd.Path = %q, want configured binary %q", cmd.Path, runner.binaryPath)
	}
	wantArgs := []string{runner.binaryPath, "--json", "snapshot"}
	assertArgs(t, cmd.Args, wantArgs)
}

func testBrowserConfig() config.BrowserAgentConfig {
	return config.BrowserAgentConfig{
		Enabled:    true,
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

func browserTaskContext(taskID string) context.Context {
	ctx := context.Background()
	ctx = tool.WithAgentScope(ctx, tool.AgentScope{TaskID: taskID, Agent: "browser-agent"})
	ctx = tool.WithTaskContract(ctx, tool.TaskContract{
		TaskID: taskID,
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"content", "source"},
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
				"source": map[string]any{
					"type": "string",
					"enum": []string{"browser", "partial"},
				},
				"notes":    map[string]any{"type": "string"},
				"evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	})
	return ctx
}

func seedSkillRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	refDir := filepath.Join(root, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "session-management.md"), []byte("SESSION REFERENCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "not-md.txt"), []byte("NO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("MAIN"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func typesMessageWithTool(name string) types.Message {
	return types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{{
			Type:       types.ContentTypeToolUse,
			ToolUseID:  "toolu_1",
			ToolName:   name,
			ToolInput:  `{}`,
			ToolResult: "",
		}},
	}
}
