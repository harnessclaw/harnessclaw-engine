package browser_agent

import (
	"testing"

	"harnessclaw-go/internal/agent"
	browseragenttool "harnessclaw-go/internal/tool/browseragent"
)

func TestBrowserAgentApprovedTools_ExpandsInternalToolsWhenParentApprovedBrowserAgent(t *testing.T) {
	def := agent.BrowserAgentDefinition()
	approved := browserAgentApprovedTools([]string{browseragenttool.ToolName}, def)

	for _, want := range []string{
		browseragenttool.ToolName,
		"browser_session_create",
		"agent_browser_command",
		"browser_agent_final_result",
		"escalate_to_planner",
	} {
		if !containsTool(approved, want) {
			t.Fatalf("approved tools missing %q: %v", want, approved)
		}
	}
}

func TestBrowserAgentApprovedTools_DoesNotExpandWithoutParentBrowserAgentApproval(t *testing.T) {
	approved := browserAgentApprovedTools([]string{"bash"}, agent.BrowserAgentDefinition())

	if containsTool(approved, "agent_browser_command") {
		t.Fatalf("approved tools should not include browser internals without parent browser_agent approval: %v", approved)
	}
}
