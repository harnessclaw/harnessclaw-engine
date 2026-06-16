package loopruntime

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	browseragentdef "harnessclaw-go/internal/engine/agent/builtin/browser_agent"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/provider/mock"
	"harnessclaw-go/internal/provider/retry"
	tool "harnessclaw-go/internal/tools"
	browsertools "harnessclaw-go/internal/tools/builtin/browser"
	pkgtypes "harnessclaw-go/pkg/types"
)

func TestLLM_BrowserAgentBindsClientMetadataBeforeCommand(t *testing.T) {
	const (
		browserSessionID = "browser_123"
		activeTabID      = "tab_browser_123"
		cliSessionName   = "harnessclaw-browser-browser_123"
		cdpEndpoint      = "ws://127.0.0.1:54395/devtools/page/page-123"
	)

	runner := &recordingBrowserRunner{}
	cfg := config.BrowserAgentConfig{
		Enabled:    true,
		CLITimeout: time.Second,
	}

	toolReg := tool.NewRegistry()
	for _, tl := range []tool.Tool{
		browsertools.NewSessionCreateTool(cfg),
		browsertools.NewSessionStateTool(cfg),
		browsertools.NewSessionCloseTool(cfg),
		browsertools.NewAgentBrowserCommandTool(cfg, runner),
		browsertools.NewFinalResultTool(),
	} {
		if err := toolReg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.Name(), err)
		}
	}

	prov := mock.New(
		mock.Response{
			ToolCalls: []pkgtypes.ToolCall{{
				ID:    "toolu_create",
				Name:  browsertools.SessionCreateToolName,
				Input: `{"visibility":"hidden"}`,
			}},
		},
		mock.Response{
			ToolCalls: []pkgtypes.ToolCall{{
				ID:    "toolu_snapshot",
				Name:  browsertools.AgentBrowserCommandToolName,
				Input: `{"args":["snapshot"]}`,
			}},
		},
	)

	sessMgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	rt := NewLLM(LLMArgs{
		Provider:      prov,
		Registry:      toolReg,
		SessionMgr:    sessMgr,
		Compactor:     nil,
		Retryer:       retry.New(nil, zap.NewNop()),
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Logger:        zap.NewNop(),
		Cfg: Config{
			MaxTokens:           1024,
			ContextWindow:       8192,
			ToolTimeout:         30 * time.Second,
			LLMAPITimeout:       30 * time.Second,
			LLMFirstByteTimeout: 30 * time.Second,
			RootDir:             t.TempDir(),
		},
	})

	events, err := rt.Run(context.Background(), runtime.RunParams{
		AgentID:    "a-browser-runtime-binding",
		Definition: *browseragentdef.BrowserAgentDefinition(),
		Prompt:     "open a browser and snapshot it",
		Overrides:  runtime.Overrides{MaxTurns: 2},
	})
	if err != nil {
		t.Fatalf("Run returned err: %v", err)
	}

	sawCreateAwait := false
	timeout := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto done
			}
			if evt.Type != pkgtypes.EngineEventToolCall || evt.ToolName != browsertools.SessionCreateToolName {
				continue
			}
			sawCreateAwait = true
			awaitSession := sessMgr.Get(evt.AwaitSessionID)
			if awaitSession == nil {
				t.Fatalf("await session %q not found", evt.AwaitSessionID)
			}
			if err := awaitSession.Awaits.ResolveTool(&pkgtypes.ToolResultPayload{
				ToolUseID: evt.ToolUseID,
				Status:    "success",
				Output:    `{"session_id":"browser_123","window_id":42,"visible":false,"active_tab":{"tab_id":"tab_browser_123","title":"Blank","url":"about:blank","active":true},"tabs":[{"tab_id":"tab_browser_123","title":"Blank","url":"about:blank","active":true}]}`,
				Metadata: map[string]any{
					"session_id":                 browserSessionID,
					"active_tab_id":              activeTabID,
					"agent_browser_session_name": cliSessionName,
					"cdp_endpoint":               cdpEndpoint,
				},
			}); err != nil {
				t.Fatalf("resolve create tool result: %v", err)
			}
		case <-timeout:
			t.Fatal("Run did not close events channel within 5s")
		}
	}

done:
	if !sawCreateAwait {
		t.Fatal("expected browser_session_create to route through client await")
	}

	gotArgs := runner.lastArgs()
	wantArgs := []string{"--session", cliSessionName, "--cdp", cdpEndpoint, "--json", "snapshot"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("agent_browser_command args mismatch\n got: %#v\nwant: %#v", gotArgs, wantArgs)
	}
}

type recordingBrowserRunner struct {
	mu   sync.Mutex
	args [][]string
}

func (r *recordingBrowserRunner) Run(_ context.Context, args []string) ([]byte, error) {
	copied := append([]string(nil), args...)
	r.mu.Lock()
	r.args = append(r.args, copied)
	r.mu.Unlock()
	return []byte(`{"success":true,"data":{"snapshot":"ok"}}`), nil
}

func (r *recordingBrowserRunner) lastArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.args) == 0 {
		return nil
	}
	return append([]string(nil), r.args[len(r.args)-1]...)
}
