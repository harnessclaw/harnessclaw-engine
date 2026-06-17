package browser_agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skills"
	"harnessclaw-go/internal/tools"
	browsertools "harnessclaw-go/internal/tools/builtin/browser"
	"harnessclaw-go/pkg/types"
)

type moduleSequenceProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *moduleSequenceProvider) Name() string { return "module-sequence" }

func (p *moduleSequenceProvider) CountTokens(context.Context, []types.Message) (int, error) {
	return 0, nil
}

func (p *moduleSequenceProvider) Chat(context.Context, *provider.ChatRequest) (*provider.ChatStream, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	events := make(chan types.StreamEvent, 2)
	switch call {
	case 1:
		events <- toolUseEvent("tu_create", browsertools.SessionCreateToolName, `{"intent":"create session","visibility":"hidden"}`)
	case 2:
		events <- toolUseEvent("tu_command", browsertools.AgentBrowserCommandToolName, `{"intent":"snapshot","args":["snapshot"]}`)
	default:
		events <- toolUseEvent("tu_final", browsertools.FinalResultToolName, `{"result":{"content":"done","source":"browser"}}`)
	}
	events <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: "tool_use",
		Usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
	}
	close(events)
	return &provider.ChatStream{Events: events, Err: func() error { return nil }}, nil
}

func toolUseEvent(id, name, input string) types.StreamEvent {
	return types.StreamEvent{
		Type: types.StreamEventToolUse,
		ToolCall: &types.ToolCall{
			ID:    id,
			Name:  name,
			Input: input,
		},
	}
}

type recordingModuleRunner struct {
	mu   sync.Mutex
	args []string
	out  []byte
}

func (r *recordingModuleRunner) Run(_ context.Context, args []string) ([]byte, error) {
	r.mu.Lock()
	r.args = append([]string(nil), args...)
	r.mu.Unlock()
	return r.out, nil
}

func (r *recordingModuleRunner) Args() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.args...)
}

type moduleSkillProvider struct{}

func (moduleSkillProvider) Load(context.Context) (*skill.SkillFull, error) {
	return &skill.SkillFull{
		SkillCard: skill.SkillCard{Name: "agent-browser/core", Version: "test", Path: "test://agent-browser"},
		Body:      "Use browser_session_create first, then agent_browser_command.",
	}, nil
}

func TestModuleRun_BindsClientBrowserMetadataBeforeLaterCommandTurn(t *testing.T) {
	t.Setenv("CLAUDE_TOOLS_BROWSER_AGENT_BINARY_PATH", "/bin/echo")

	cfg := config.BrowserAgentConfig{Enabled: true, CLITimeout: time.Second}
	runner := &recordingModuleRunner{out: []byte(`{"success":true,"data":{"snapshot":"ok"}}`)}

	reg := tool.NewRegistry()
	for _, tl := range []tool.Tool{
		browsertools.NewSessionCreateTool(cfg),
		browsertools.NewSessionStateTool(cfg),
		browsertools.NewSessionCloseTool(cfg),
		browsertools.NewAgentBrowserCommandTool(cfg, runner),
		browsertools.NewFinalResultTool(),
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.Name(), err)
		}
	}

	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	mod := New(Deps{
		Provider:           &moduleSequenceProvider{},
		Registry:           reg,
		SessionMgr:         mgr,
		Retryer:            retry.New(retry.DefaultConfig(), zap.NewNop()),
		Logger:             zap.NewNop(),
		MaxTokens:          100,
		ContextWindow:      200000,
		BrowserAgentConfig: cfg,
		SkillProvider:      moduleSkillProvider{},
	})

	out := make(chan types.EngineEvent, 32)
	done := make(chan *common.SpawnResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := mod.Run(context.Background(), &common.SpawnConfig{
			Prompt:          "take a snapshot",
			AgentType:       tool.AgentTypeSync,
			ParentSessionID: "root_session_without_active_record",
			ParentOut:       out,
			TaskID:          "browser_task_1",
			MaxTurns:        2,
		})
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	for {
		select {
		case err := <-errs:
			t.Fatalf("module Run: %v", err)
		case <-done:
			gotArgs := runner.Args()
			wantArgs := []string{
				"--session", "harnessclaw-browser-browser_session_123",
				"--cdp", "ws://127.0.0.1:62957/devtools/page/page-1",
				"--json",
				"snapshot",
			}
			if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
				t.Fatalf("agent_browser_command args = %v, want %v", gotArgs, wantArgs)
			}
			return
		case ev := <-out:
			if ev.Type != types.EngineEventToolCall {
				continue
			}
			if ev.ToolName != browsertools.SessionCreateToolName {
				t.Fatalf("unexpected client tool call: %+v", ev)
			}
			awaitSession := mgr.Get(ev.AwaitSessionID)
			if awaitSession == nil {
				t.Fatalf("await session %q not found", ev.AwaitSessionID)
			}
			if err := awaitSession.Awaits.ResolveTool(&types.ToolResultPayload{
				ToolUseID: ev.ToolUseID,
				Status:    "success",
				Output:    `{"session_id":"browser_session_123","window_id":"1","visible":false,"active_tab":{"tab_id":"tab_1","title":"ok","url":"https://example.com","active":true},"tabs":[{"tab_id":"tab_1","title":"ok","url":"https://example.com","active":true}]}`,
				Metadata: map[string]any{
					"session_id":                 "browser_session_123",
					"active_tab_id":              "tab_1",
					"agent_browser_session_name": "harnessclaw-browser-browser_session_123",
					"cdp_endpoint":               "ws://127.0.0.1:62957/devtools/page/page-1",
				},
			}); err != nil {
				t.Fatalf("resolve client tool result: %v", err)
			}
		case <-time.After(2 * time.Second):
			args, _ := json.Marshal(runner.Args())
			t.Fatalf("timeout waiting for module completion; runner args=%s", args)
		}
	}
}
