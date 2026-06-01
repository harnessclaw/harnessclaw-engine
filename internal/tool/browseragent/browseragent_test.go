package browseragent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type fakeSpawner struct {
	cfg *agent.SpawnConfig
}

func (s *fakeSpawner) SpawnSync(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	copied := *cfg
	s.cfg = &copied
	return &agent.SpawnResult{
		Output:    "browser result",
		SessionID: "sub_1",
		AgentID:   "agent_1",
		Terminal:  &types.Terminal{Reason: types.TerminalCompleted},
	}, nil
}

func TestBrowserAgentTool_LongRunningAndSafety(t *testing.T) {
	tl := New(&fakeSpawner{}, config.BrowserAgentConfig{Enabled: true, MaxSteps: 30}, zap.NewNop())

	if tl.Name() != "browser_agent" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	if !tl.IsLongRunning() {
		t.Fatal("browser_agent must be long-running")
	}
	if got := tool.EffectiveSafetyLevel(tl); got != tool.SafetyDangerous {
		t.Fatalf("safety = %s, want %s", got, tool.SafetyDangerous)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"goal":"read the rendered page","start_url":"https://example.com","max_steps":8}`)); err != nil {
		t.Fatalf("ValidateInput valid: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{"goal":"   "}`)); err == nil {
		t.Fatal("blank goal should be rejected")
	}
}

func TestBrowserAgentTool_DescriptionAdvertisesRealBrowserSubAgent(t *testing.T) {
	tl := New(&fakeSpawner{}, config.BrowserAgentConfig{Enabled: true, MaxSteps: 30}, zap.NewNop())

	desc := tl.Description()
	for _, want := range []string{
		"当用户询问是否能使用浏览器",
		"browser_agent",
		"browser-agent 子 Agent",
		"真实浏览器",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestBrowserAgentTool_PromptRequestsConfiguredVisibleSession(t *testing.T) {
	prompt := buildPrompt(input{
		Goal:     "read the rendered page",
		StartURL: "https://example.com",
	}, 8, config.BrowserAgentConfig{DefaultVisibility: "visible"})

	for _, want := range []string{
		`visibility="visible"`,
		"台前",
		"浏览器窗口",
		"browser_navigate",
		`submit_task_result`,
		`result`,
		`content`,
		`source`,
		"全局持久 profile",
		"关闭窗口后继续复用",
		"不要传 task_id 或 partition",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"创建时传入 start_url",
		"meta_path",
		"meta.json",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt should not mention %q:\n%s", forbidden, prompt)
		}
	}
}

func TestBrowserAgentTool_RejectsBlockedStartURL(t *testing.T) {
	tl := New(&fakeSpawner{}, config.BrowserAgentConfig{
		Enabled:        true,
		MaxSteps:       30,
		BlockedDomains: []string{"blocked.example"},
	}, zap.NewNop())

	err := tl.ValidateInput(json.RawMessage(`{"goal":"read","start_url":"https://blocked.example/page"}`))
	if err == nil {
		t.Fatal("blocked start_url should be rejected")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error = %v, want blocked domain", err)
	}
}

func TestBrowserAgentTool_SpawnsBrowserSubAgent(t *testing.T) {
	spawner := &fakeSpawner{}
	tl := New(spawner, config.BrowserAgentConfig{Enabled: true, MaxSteps: 30}, zap.NewNop())

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"goal":"collect prices","start_url":"https://example.com","max_steps":8}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned error result: %+v", res)
	}
	if res.Content != "browser result" {
		t.Fatalf("content = %q", res.Content)
	}
	if spawner.cfg == nil {
		t.Fatal("SpawnSync was not called")
	}
	if spawner.cfg.SubagentType != agent.BrowserAgentName {
		t.Fatalf("SubagentType = %q", spawner.cfg.SubagentType)
	}
	if spawner.cfg.Name != agent.BrowserAgentName {
		t.Fatalf("Name = %q", spawner.cfg.Name)
	}
	if spawner.cfg.MaxTurns != 8 {
		t.Fatalf("MaxTurns = %d, want 8", spawner.cfg.MaxTurns)
	}
	if spawner.cfg.Timeout != 5*time.Minute {
		t.Fatalf("Timeout = %s, want 5m", spawner.cfg.Timeout)
	}
	if !strings.Contains(spawner.cfg.Prompt, "collect prices") || !strings.Contains(spawner.cfg.Prompt, "https://example.com") {
		t.Fatalf("prompt missing goal/start_url: %q", spawner.cfg.Prompt)
	}
}
