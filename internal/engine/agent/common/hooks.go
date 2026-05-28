package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

// StopOnEndTurn returns a TurnHook that terminates as soon as the
// assistant message contains no tool_use blocks (i.e. natural end_turn).
//
// Used by tier modules that do not require a structured submit step:
// the LLM "finishes" by simply not calling any tool on its final turn.
func StopOnEndTurn() loop.TurnHook {
	return func(turn int, msg types.Message, _ []types.ToolResult) loop.Decision {
		if hasToolCalls(msg) {
			return loop.Decision{}
		}
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: turn,
		}}
	}
}

// StopOnSubmitResult returns a TurnHook that terminates the first time
// the LLM calls submit_task_result. A natural end_turn (no tool calls)
// is also treated as completion so a stuck LLM doesn't spin to MaxTurns.
// No schema enforcement; use ContractEnforcer for that.
func StopOnSubmitResult() loop.TurnHook {
	return func(turn int, msg types.Message, _ []types.ToolResult) loop.Decision {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeToolUse && b.ToolName == "submit_task_result" {
				return loop.Decision{Terminate: &types.Terminal{
					Reason: types.TerminalCompleted, Turn: turn,
				}}
			}
		}
		if !hasToolCalls(msg) {
			return loop.Decision{Terminate: &types.Terminal{
				Reason: types.TerminalCompleted, Turn: turn,
			}}
		}
		return loop.Decision{}
	}
}

// ContractEnforcer returns a TurnHook that validates submit_task_result
// against the supplied ExpectedOutputs. Behavior:
//   - submit not present + other tool calls + budget remaining → continue
//   - submit not present + other tool calls + budget exhausted → inject
//     "you have N turns left, submit now" hard nudge (max once, fires when
//     turn ≥ maxTurns - 2). Without this the LLM can spend the entire
//     loop in tool_use and ContractEnforcer never gets a chance to nudge,
//     because the natural-end_turn branch below only fires on bare
//     assistant messages. Observed: a freelancer drafting a docx burned
//     all 25 turns iterating edit/bash/write and never reached submit;
//     dispatch.out returned content_len=0 and the L2 parent had no
//     summary or artifact pointers to read.
//   - submit not present + no tool calls    → inject "please submit" nudge
//   - submit present + valid                → Terminate completed
//   - submit present + invalid              → inject correction tool_result, continue
//   - retries exhausted                     → Terminate completed (message records failure)
//
// The retry counter is held in closure state and survives across turns
// for the lifetime of this hook instance. maxRetries < 1 is normalized
// to 1 so callers cannot accidentally disable enforcement. maxTurns ≤ 0
// disables the budget-exhaustion nudge (legacy "no cap" behaviour).
func ContractEnforcer(expected []types.ExpectedOutput, maxRetries, maxTurns int) loop.TurnHook {
	return submitResultEnforcer(expected, nil, maxRetries, maxTurns)
}

// SubmitResultEnforcer validates submit_task_result completion.
// expected covers file-producing agents; outputSchema covers
// structured-result agents such as browser-agent.
func SubmitResultEnforcer(expected []types.ExpectedOutput, outputSchema map[string]any, maxRetries int) loop.TurnHook {
	return submitResultEnforcer(expected, outputSchema, maxRetries, 0)
}

func submitResultEnforcer(expected []types.ExpectedOutput, outputSchema map[string]any, maxRetries, maxTurns int) loop.TurnHook {
	if maxRetries < 1 {
		maxRetries = 1
	}
	failures := 0
	hardNudgeSent := false

	return func(turn int, msg types.Message, toolResults []types.ToolResult) loop.Decision {
		submitCall, submitResult := findSubmitCall(msg, toolResults)
		if submitCall == nil {
			// No submit yet. If the LLM also issued no tool calls at all,
			// it has likely stopped — nudge it back toward submitting.
			if !hasToolCalls(msg) {
				return loop.Decision{Inject: []types.Message{{
					Role: types.RoleUser,
					Content: []types.ContentBlock{{
						Type: types.ContentTypeText,
						Text: "Please submit your result via submit_task_result.",
					}},
				}}}
			}
			// Budget-exhaustion nudge: if the loop is within 2 turns of
			// MaxTurns and we have not nudged yet, force the LLM to stop
			// new work and submit. Sent at most once per hook instance.
			if !hardNudgeSent && maxTurns > 0 && turn >= maxTurns-2 {
				hardNudgeSent = true
				remaining := maxTurns - turn
				if remaining < 1 {
					remaining = 1
				}
				return loop.Decision{Inject: []types.Message{{
					Role: types.RoleUser,
					Content: []types.ContentBlock{{
						Type: types.ContentTypeText,
						Text: fmt.Sprintf(
							"You have only %d turn(s) left before this sub-agent is terminated. "+
								"Stop any new work immediately. Your next assistant message MUST "+
								"call meta_write({status, summary, outputs}) followed by "+
								"submit_task_result({}) — even if the deliverable is incomplete, "+
								"report what exists. If you do not submit, the parent agent will "+
								"see content_len=0 and treat the task as failed.",
							remaining,
						),
					}},
				}}}
			}
			return loop.Decision{}
		}

		if accepted, ok := submitAccepted(submitResult); ok {
			if accepted {
				return loop.Decision{Terminate: &types.Terminal{
					Reason: types.TerminalCompleted, Turn: turn,
				}}
			}
			failures++
			if failures > maxRetries {
				return loop.Decision{Terminate: &types.Terminal{
					Reason:  types.TerminalCompleted,
					Message: "contract validation exhausted retries: " + submitRejectionReason(submitResult),
					Turn:    turn,
				}}
			}
			return loop.Decision{Inject: []types.Message{{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type:       types.ContentTypeToolResult,
					ToolUseID:  submitCall.ToolUseID,
					ToolName:   "submit_task_result",
					ToolResult: "submit_task_result rejected: " + submitRejectionReason(submitResult) + ". Please call submit_task_result again with the required fields.",
					IsError:    true,
				}},
			}}}
		}

		if err := validateSubmitInput(submitCall.ToolInput, expected); err != nil {
			failures++
			if failures > maxRetries {
				return loop.Decision{Terminate: &types.Terminal{
					Reason:  types.TerminalCompleted,
					Message: "contract validation exhausted retries: " + err.Error(),
					Turn:    turn,
				}}
			}
			correction := fmt.Sprintf(
				"submit_task_result rejected: %s. Please call submit_task_result again with the required fields.",
				err.Error(),
			)
			return loop.Decision{Inject: []types.Message{{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type:       types.ContentTypeToolResult,
					ToolUseID:  submitCall.ToolUseID,
					ToolName:   "submit_task_result",
					ToolResult: correction,
					IsError:    true,
				}},
			}}}
		}
		if err := validateStructuredResultInput(submitCall.ToolInput, outputSchema); err != nil {
			failures++
			if failures > maxRetries {
				return loop.Decision{Terminate: &types.Terminal{
					Reason:  types.TerminalCompleted,
					Message: "contract validation exhausted retries: " + err.Error(),
					Turn:    turn,
				}}
			}
			correction := fmt.Sprintf(
				"submit_task_result rejected: %s. Please call submit_task_result again with the required fields.",
				err.Error(),
			)
			return loop.Decision{Inject: []types.Message{{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type:       types.ContentTypeToolResult,
					ToolUseID:  submitCall.ToolUseID,
					ToolName:   "submit_task_result",
					ToolResult: correction,
					IsError:    true,
				}},
			}}}
		}

		// Schema OK → terminate.
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: turn,
		}}
	}
}

// hasToolCalls reports whether the assistant message contains any
// tool_use blocks.
func hasToolCalls(msg types.Message) bool {
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeToolUse {
			return true
		}
	}
	return false
}

// findSubmitCall returns a pointer to the first submit_task_result
// tool_use block in the message plus its aligned tool result, when the
// tool was executed in this turn.
func findSubmitCall(msg types.Message, toolResults []types.ToolResult) (*types.ContentBlock, *types.ToolResult) {
	toolResultIdx := 0
	for i, b := range msg.Content {
		if b.Type != types.ContentTypeToolUse {
			continue
		}
		var result *types.ToolResult
		if toolResultIdx < len(toolResults) {
			result = &toolResults[toolResultIdx]
		}
		toolResultIdx++
		if b.ToolName == "submit_task_result" {
			return &msg.Content[i], result
		}
	}
	return nil, nil
}

func submitAccepted(result *types.ToolResult) (bool, bool) {
	if result == nil || result.Metadata == nil {
		return false, false
	}
	if hint, _ := result.Metadata["render_hint"].(string); hint != submittool.MetadataRenderHint {
		return false, false
	}
	accepted, ok := result.Metadata[submittool.MetadataKeyAccepted].(bool)
	return accepted, ok
}

func submitRejectionReason(result *types.ToolResult) string {
	if result == nil {
		return "missing submit_task_result tool result"
	}
	if result.Metadata != nil {
		if reason, _ := result.Metadata["reason"].(string); strings.TrimSpace(reason) != "" {
			return strings.TrimSpace(reason)
		}
	}
	if strings.TrimSpace(result.Content) != "" {
		return strings.TrimSpace(result.Content)
	}
	return "unknown rejection"
}

// validateSubmitInput parses the submit_task_result tool input (a JSON
// string per types.ContentBlock.ToolInput) and verifies that every
// Required ExpectedOutput.Role appears in the artifacts array.
//
// Empty expected → no constraints (validation passes).
func validateSubmitInput(rawInput string, expected []types.ExpectedOutput) error {
	if len(expected) == 0 {
		return nil
	}
	if rawInput == "" {
		return fmt.Errorf("submit input is empty")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rawInput), &parsed); err != nil {
		return fmt.Errorf("submit input not valid JSON: %w", err)
	}
	artifactsAny, ok := parsed["artifacts"]
	if !ok {
		return fmt.Errorf("missing 'artifacts' array")
	}
	artifacts, ok := artifactsAny.([]any)
	if !ok {
		return fmt.Errorf("'artifacts' must be array")
	}

	requiredRoles := make(map[string]bool)
	for _, e := range expected {
		if e.Required {
			requiredRoles[e.Role] = false
		}
	}
	for _, a := range artifacts {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if _, ok := requiredRoles[role]; ok {
			requiredRoles[role] = true
		}
	}
	var missing []string
	for role, satisfied := range requiredRoles {
		if !satisfied {
			missing = append(missing, role)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required artifact roles: %v", missing)
	}
	return nil
}

func validateStructuredResultInput(rawInput string, outputSchema map[string]any) error {
	if len(outputSchema) == 0 {
		return nil
	}
	if rawInput == "" {
		return fmt.Errorf("submit input is empty")
	}
	var parsed struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(rawInput), &parsed); err != nil {
		return fmt.Errorf("submit input not valid JSON: %w", err)
	}
	if failures := submittool.ValidateAgainstSchema(outputSchema, parsed.Result); len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}
