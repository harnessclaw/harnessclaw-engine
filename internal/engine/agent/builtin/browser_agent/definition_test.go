package browser_agent

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
)

func TestBrowserAgentDefinition(t *testing.T) {
	def := BrowserAgentDefinition()

	if def.Name != AgentName {
		t.Fatalf("Name = %q", def.Name)
	}
	if def.Tier != definition.TierSubAgent {
		t.Fatalf("Tier = %q, want %q", def.Tier, definition.TierSubAgent)
	}
	if def.AgentType != tool.AgentTypeSync {
		t.Fatalf("AgentType = %q, want sync", def.AgentType)
	}
	for _, want := range []string{
		"browser_session_create",
		"browser_session_state",
		"browser_session_close",
		"browser_ask_human",
		"agent_browser_command",
		"browser_skill_reference",
		"browser_agent_final_result",
		"escalate_to_planner",
	} {
		if !containsString(def.AllowedTools, want) {
			t.Fatalf("browser agent AllowedTools missing %q: %v", want, def.AllowedTools)
		}
	}
	for _, forbidden := range []string{
		"browser_navigate",
		"browser_snapshot",
		"browser_extract",
		"browser_click",
		"browser_fill",
		"browser_press",
		"browser_scroll",
		"browser_screenshot",
		"browser_back",
		"browser_wait",
		"browser_tabs",
		"web_search",
		"tavily_search",
		"web_fetch",
		"submit_task_result",
	} {
		if containsString(def.AllowedTools, forbidden) {
			t.Fatalf("browser agent AllowedTools should not expose old wrapper %q: %v", forbidden, def.AllowedTools)
		}
	}
	if len(def.OutputSchema) == 0 {
		t.Fatal("OutputSchema is required for TierSubAgent")
	}
	for _, want := range []string{
		"submit_task_result",
		"result",
		"content",
		"source",
		"browser_session_state",
		"登录",
		"隐藏",
		"不要主动关闭",
		"全局持久 profile",
		"关闭窗口后继续复用",
	} {
		if !strings.Contains(def.SystemPrompt, want) {
			t.Fatalf("browser-agent prompt missing %q:\n%s", want, def.SystemPrompt)
		}
	}
	for _, forbidden := range []string{
		"meta_path",
		"meta.json",
		"meta_write",
	} {
		if strings.Contains(def.SystemPrompt, forbidden) {
			t.Fatalf("browser-agent prompt should not mention %q:\n%s", forbidden, def.SystemPrompt)
		}
	}

	reg := definition.NewRegistry()
	if err := reg.Register(def); err != nil {
		t.Fatalf("Register browser-agent: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
