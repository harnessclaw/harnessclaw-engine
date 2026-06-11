package common_test

import (
	"testing"

	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/tools/builtin/submittool"
	"harnessclaw-go/pkg/types"
)

// snap builds a TurnSnapshot from the old (turn, msg, results) triple so
// existing test bodies translate one-to-one. HadToolCalls is derived from
// msg.Content to match what loop.Run would compute.
func snap(turn int, msg types.Message, results []types.ToolResult) loop.TurnSnapshot {
	hasTC := false
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeToolUse {
			hasTC = true
			break
		}
	}
	return loop.TurnSnapshot{
		Turn:         turn,
		AssistantMsg: msg,
		ToolResults:  results,
		HadToolCalls: hasTC,
	}
}

func TestStopOnEndTurn_TerminatesWhenNoToolCalls(t *testing.T) {
	hook := common.StopOnEndTurn()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "done"},
	}}
	d := hook(snap(1, msg, nil))
	if d.Terminate == nil {
		t.Fatal("expected Terminate non-nil")
	}
	if d.Terminate.Reason != types.TerminalCompleted {
		t.Errorf("Reason = %v, want Completed", d.Terminate.Reason)
	}
}

func TestStopOnEndTurn_ContinuesWhenToolCallsPresent(t *testing.T) {
	hook := common.StopOnEndTurn()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "t1", ToolName: "read"},
	}}
	d := hook(snap(1, msg, []types.ToolResult{{Content: "ok"}}))
	if d.Terminate != nil {
		t.Errorf("expected continue, got terminate %v", d.Terminate)
	}
}

func TestStopOnSubmitResult_TerminatesOnSubmit(t *testing.T) {
	hook := common.StopOnSubmitResult()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "s1", ToolName: "submit_task_result", ToolInput: `{}`},
	}}
	d := hook(snap(1, msg, nil))
	if d.Terminate == nil {
		t.Fatal("expected terminate when submit_task_result called")
	}
	if d.Terminate.Reason != types.TerminalCompleted {
		t.Errorf("Reason = %v, want Completed", d.Terminate.Reason)
	}
}

func TestStopOnSubmitResult_TerminatesOnEndTurn(t *testing.T) {
	hook := common.StopOnSubmitResult()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "all done"},
	}}
	d := hook(snap(1, msg, nil))
	if d.Terminate == nil {
		t.Fatal("expected terminate on natural end_turn")
	}
}

func TestStopOnSubmitResult_ContinuesOnOtherTool(t *testing.T) {
	hook := common.StopOnSubmitResult()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "r1", ToolName: "read", ToolInput: `{"path":"foo"}`},
	}}
	d := hook(snap(1, msg, []types.ToolResult{{Content: "ok"}}))
	if d.Terminate != nil {
		t.Errorf("expected continue for non-submit tool, got %v", d.Terminate)
	}
}

func TestContractEnforcer_AcceptsValidSubmit(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2, 25)

	goodSubmit := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolName: "submit_task_result",
			ToolUseID: "s1", ToolInput: `{"artifacts":[{"role":"result","path":"out.md"}]}`},
	}}
	d := enforcer(snap(1, goodSubmit, []types.ToolResult{{Content: "ok"}}))
	if d.Terminate == nil {
		t.Fatal("expected terminate on valid submit")
	}
	if d.Terminate.Reason != types.TerminalCompleted {
		t.Errorf("Reason = %v, want Completed", d.Terminate.Reason)
	}
}

func TestContractEnforcer_RetryUntilLimitThenFail(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, /*maxRetries*/ 2, /*maxTurns*/ 25)

	badSubmit := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolName: "submit_task_result",
			ToolUseID: "s1", ToolInput: `{"invalid":true}`},
	}}
	badResult := []types.ToolResult{{Content: "ok", IsError: false}}

	d1 := enforcer(snap(1, badSubmit, badResult))
	if d1.Terminate != nil {
		t.Errorf("turn 1 should inject correction, not terminate; got %v", d1.Terminate)
	}
	if len(d1.Inject) == 0 {
		t.Error("turn 1 should inject correction message")
	}

	d2 := enforcer(snap(2, badSubmit, badResult))
	if d2.Terminate != nil {
		t.Error("turn 2 should still retry")
	}

	d3 := enforcer(snap(3, badSubmit, badResult))
	if d3.Terminate == nil {
		t.Fatal("turn 3 should terminate after exhausting retries")
	}
}

func TestContractEnforcer_NudgesWhenNoToolCalls(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2, 25)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "I'm thinking..."},
	}}
	d := enforcer(snap(1, msg, nil))
	if d.Terminate != nil {
		t.Errorf("should not terminate when no submit yet; got %v", d.Terminate)
	}
	if len(d.Inject) == 0 {
		t.Error("expected nudge injection when LLM stops without submitting")
	}
}

func TestContractEnforcer_HardNudgeOnBudgetExhaustion(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2, /*maxTurns*/ 10)

	// LLM is busy with non-submit tools mid-loop — no nudge.
	busyMsg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "e1", ToolName: "edit", ToolInput: `{}`},
	}}
	if d := enforcer(snap(5, busyMsg, []types.ToolResult{{Content: "ok"}})); len(d.Inject) != 0 {
		t.Errorf("mid-loop tool_use should not trigger nudge; got %d injects", len(d.Inject))
	}

	// turn 9 = maxTurns - 1, still in tool_use → hard nudge fires.
	d := enforcer(snap(9, busyMsg, []types.ToolResult{{Content: "ok"}}))
	if len(d.Inject) != 1 {
		t.Fatalf("turn 9 with tool_use should inject hard nudge; got %d injects", len(d.Inject))
	}
	if got := d.Inject[0].Content[0].Text; !contains(got, "submit_task_result") {
		t.Errorf("hard nudge should reference submit_task_result; got %q", got)
	}

	// Already nudged — turn 10 must not nudge again.
	d2 := enforcer(snap(10, busyMsg, []types.ToolResult{{Content: "ok"}}))
	if len(d2.Inject) != 0 {
		t.Errorf("hard nudge should fire at most once; turn 10 got %d injects", len(d2.Inject))
	}
}

func TestContractEnforcer_NoHardNudgeWhenMaxTurnsZero(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2, /*maxTurns*/ 0)
	busyMsg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "e1", ToolName: "edit", ToolInput: `{}`},
	}}
	for _, turn := range []int{1, 50, 9999} {
		if d := enforcer(snap(turn, busyMsg, []types.ToolResult{{Content: "ok"}})); len(d.Inject) != 0 {
			t.Errorf("maxTurns=0 must disable hard nudge; turn %d got %d injects", turn, len(d.Inject))
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestContractEnforcer_ContinuesOnOtherTool(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2, 25)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "r1", ToolName: "read", ToolInput: `{}`},
	}}
	d := enforcer(snap(1, msg, []types.ToolResult{{Content: "ok"}}))
	if d.Terminate != nil {
		t.Errorf("should continue when non-submit tool called; got %v", d.Terminate)
	}
	if len(d.Inject) != 0 {
		t.Errorf("should not inject when LLM is using other tools; got %d injects", len(d.Inject))
	}
}

func TestSubmitResultEnforcer_AcceptsStructuredSubmitToolResult(t *testing.T) {
	enforcer := common.SubmitResultEnforcer(nil, map[string]any{
		"type":     "object",
		"required": []string{"content", "source"},
	}, 2)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "s1", ToolName: "submit_task_result", ToolInput: `{"task_id":"browser_1","result":{"content":"done","source":"direct_access"}}`},
	}}
	results := []types.ToolResult{{
		Content: `{"status":"accepted"}`,
		Metadata: map[string]any{
			"render_hint":                  submittool.MetadataRenderHint,
			submittool.MetadataKeyAccepted: true,
			"task_id":                      "browser_1",
			"result":                       map[string]any{"content": "done", "source": "direct_access"},
		},
	}}

	d := enforcer(snap(1, msg, results))
	if d.Terminate == nil {
		t.Fatal("expected accepted submit result to terminate")
	}
	if d.Terminate.Reason != types.TerminalCompleted {
		t.Fatalf("Reason = %v, want completed", d.Terminate.Reason)
	}
}

func TestSubmitResultEnforcer_AcceptsConfiguredFinalTool(t *testing.T) {
	enforcer := common.SubmitResultEnforcerForTool("browser_agent_final_result", nil, map[string]any{
		"type":     "object",
		"required": []string{"content", "source"},
	}, 2)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "s1", ToolName: "browser_agent_final_result", ToolInput: `{"content":"done","source":"browser"}`},
	}}
	results := []types.ToolResult{{
		Content: `{"status":"accepted"}`,
		Metadata: map[string]any{
			"render_hint":                  submittool.MetadataRenderHint,
			submittool.MetadataKeyAccepted: true,
			"task_id":                      "browser_1",
			"result":                       map[string]any{"content": "done", "source": "browser"},
		},
	}}

	d := enforcer(snap(1, msg, results))
	if d.Terminate == nil {
		t.Fatal("expected accepted final result to terminate")
	}
}
