package common

import (
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/engine/loop"
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
	if maxRetries < 1 {
		maxRetries = 1
	}
	failures := 0
	hardNudgeSent := false

	return func(turn int, msg types.Message, _ []types.ToolResult) loop.Decision {
		submitCall := findSubmitCall(msg)
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
// tool_use block in the message, or nil if none exists.
func findSubmitCall(msg types.Message) *types.ContentBlock {
	for i, b := range msg.Content {
		if b.Type == types.ContentTypeToolUse && b.ToolName == "submit_task_result" {
			return &msg.Content[i]
		}
	}
	return nil
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
