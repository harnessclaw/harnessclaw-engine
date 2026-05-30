package common_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/pkg/types"
)

func TestStopOnEndTurn_TerminatesWhenNoToolCalls(t *testing.T) {
	hook := common.StopOnEndTurn()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "done"},
	}}
	d := hook(1, msg, nil)
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
	d := hook(1, msg, []types.ToolResult{{Content: "ok"}})
	if d.Terminate != nil {
		t.Errorf("expected continue, got terminate %v", d.Terminate)
	}
}

func TestStopOnSubmitResult_TerminatesOnSubmit(t *testing.T) {
	hook := common.StopOnSubmitResult()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "s1", ToolName: "submit_task_result", ToolInput: `{}`},
	}}
	d := hook(1, msg, nil)
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
	d := hook(1, msg, nil)
	if d.Terminate == nil {
		t.Fatal("expected terminate on natural end_turn")
	}
}

func TestStopOnSubmitResult_ContinuesOnOtherTool(t *testing.T) {
	hook := common.StopOnSubmitResult()
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "r1", ToolName: "read", ToolInput: `{"path":"foo"}`},
	}}
	d := hook(1, msg, []types.ToolResult{{Content: "ok"}})
	if d.Terminate != nil {
		t.Errorf("expected continue for non-submit tool, got %v", d.Terminate)
	}
}

func TestContractEnforcer_AcceptsValidSubmit(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2)

	goodSubmit := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolName: "submit_task_result",
			ToolUseID: "s1", ToolInput: `{"artifacts":[{"role":"result","path":"out.md"}]}`},
	}}
	d := enforcer(1, goodSubmit, []types.ToolResult{{Content: "ok"}})
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
	}}, /*maxRetries*/ 2)

	badSubmit := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolName: "submit_task_result",
			ToolUseID: "s1", ToolInput: `{"invalid":true}`},
	}}
	badResult := []types.ToolResult{{Content: "ok", IsError: false}}

	d1 := enforcer(1, badSubmit, badResult)
	if d1.Terminate != nil {
		t.Errorf("turn 1 should inject correction, not terminate; got %v", d1.Terminate)
	}
	if len(d1.Inject) == 0 {
		t.Error("turn 1 should inject correction message")
	}

	d2 := enforcer(2, badSubmit, badResult)
	if d2.Terminate != nil {
		t.Error("turn 2 should still retry")
	}

	d3 := enforcer(3, badSubmit, badResult)
	if d3.Terminate == nil {
		t.Fatal("turn 3 should terminate after exhausting retries")
	}
}

func TestContractEnforcer_NudgesWhenNoToolCalls(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeText, Text: "I'm thinking..."},
	}}
	d := enforcer(1, msg, nil)
	if d.Terminate != nil {
		t.Errorf("should not terminate when no submit yet; got %v", d.Terminate)
	}
	if len(d.Inject) == 0 {
		t.Error("expected nudge injection when LLM stops without submitting")
	}
}

func TestContractEnforcer_ContinuesOnOtherTool(t *testing.T) {
	enforcer := common.ContractEnforcer([]types.ExpectedOutput{{
		Role: "result", Required: true,
	}}, 2)
	msg := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ToolUseID: "r1", ToolName: "read", ToolInput: `{}`},
	}}
	d := enforcer(1, msg, []types.ToolResult{{Content: "ok"}})
	if d.Terminate != nil {
		t.Errorf("should continue when non-submit tool called; got %v", d.Terminate)
	}
	if len(d.Inject) != 0 {
		t.Errorf("should not inject when LLM is using other tools; got %d injects", len(d.Inject))
	}
}
