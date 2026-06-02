package browser_agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_IncludesOfficialSkillAndCommandGateway(t *testing.T) {
	prompt := buildSystemPrompt()

	for _, want := range []string{
		"HarnessClaw adapter:",
		"agent_browser_command",
		"browser_skill_reference",
		"<loaded-skills>",
		`<skill name="agent-browser/core"`,
		"OFFICIAL CORE SKILL BODY",
		"browser_agent_final_result",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"browser_navigate",
		"browser_snapshot",
		"browser_click",
		"browser_fill",
		"search_fallback",
		"api_fallback",
		"web_search",
		"tavily_search",
		"web_fetch",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("system prompt should not mention old wrapper %q:\n%s", forbidden, prompt)
		}
	}
}
