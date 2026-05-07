package engine

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// ReActCoordinator realises CoordinatorModeReAct: a free-form LLM agent
// loop that thinks, dispatches L3 sub-agents via the Task tool, integrates
// their results, and returns.
//
// D-mode escalation: when the ReAct run terminates with a recoverable
// failure (contract violations, partial output, judge-flagged miss), this
// coordinator can promote the run to Plan mode rather than returning a
// hard failure. The decision is gated by:
//   - shouldEscalate: looks at the loopResult's terminal reason +
//     contract failures
//   - allowEscalation: a flag (default true) that callers can disable
//     when they want pure ReAct behaviour for tests or for tasks that
//     have already been escalated once (avoid loops)
//
// Promotion is internal — the wrapping SpawnSync goroutine sees a single
// subAgentLoopResult either way; trace events distinguish the two phases
// via mode-tagged log lines.
type ReActCoordinator struct {
	deps            *SharedDeps
	allowEscalation bool
}

// NewReActCoordinator is exposed so tests can construct a coordinator
// with escalation disabled. Production callers go through the registry.
func NewReActCoordinator(deps *SharedDeps, allowEscalation bool) *ReActCoordinator {
	return &ReActCoordinator{deps: deps, allowEscalation: allowEscalation}
}

// Mode reports CoordinatorModeReAct. Used by telemetry to tag every event
// with the mode that produced it — handy when correlating "this task
// burned X tokens" with "this task ran in mode Y".
func (c *ReActCoordinator) Mode() CoordinatorMode { return CoordinatorModeReAct }

// Run delegates to QueryEngine.runSubAgentLoop, then optionally escalates
// to Plan mode if the result indicates a recoverable failure.
func (c *ReActCoordinator) Run(
	ctx context.Context,
	sess *session.Session,
	lc *loopConfig,
	out chan<- types.EngineEvent,
) subAgentLoopResult {
	result := c.deps.QE.runSubAgentLoop(ctx, sess, lc, out)

	if !c.allowEscalation {
		return result
	}

	// D-mode auto-escalation only fires for Specialists. The other
	// coordinator-tier agents (Plan, Explore, general-purpose) are
	// themselves alternative coordinators — wrapping them in Plan mode
	// would be tier confusion. The check is on the agent name (matches
	// SubagentType) rather than profile so this stays robust to
	// profile renames.
	if !escalationEligible(lc) {
		return result
	}

	if !shouldEscalate(result) {
		return result
	}

	if c.deps == nil || c.deps.Logger == nil {
		return result
	}

	if exceeded, why := c.budgetExceeded(); exceeded {
		c.deps.Logger.Warn("react: would escalate to plan but budget exhausted",
			zap.String("reason", why),
		)
		return result
	}

	c.deps.Logger.Info("react: escalating to plan mode",
		zap.String("terminal_reason", string(result.Terminal.Reason)),
		zap.Strings("contract_failures", result.ContractFailures),
		zap.Int("submitted_artifacts", len(result.SubmittedArtifacts)),
	)

	// Wire-level event so clients can see the escalation rather than
	// just observing the mode tag flip on the next event. Mirrors the
	// spawn lifecycle pattern; render_hint=coordinator.escalation.
	if out != nil {
		out <- types.EngineEvent{
			Type:      types.EngineEventText,
			Text:      "",
			ToolName:  "coordinator.escalation",
			ToolInput: `{"from":"react","to":"plan","reason":"` + escapeJSON(result.Terminal.Message) + `"}`,
		}
	}

	plan := &PlanCoordinator{
		deps:       c.deps,
		escalation: buildReActEscalation(result, c.snapshotBudget()),
	}
	planResult := plan.Run(ctx, sess, lc, out)
	planResult.EscalatedFromMode = string(CoordinatorModeReAct)
	planResult.CoordinatorMode = string(CoordinatorModePlan)
	return planResult
}

// escapeJSON minimally escapes a string for safe inline embedding into a
// JSON-shaped string literal. Tiny helper to keep the escalation event
// emitter simple — full-fledged json.Marshal would still work but adds a
// dependency to a hot path.
func escapeJSON(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}

// escalationEligible bounds D-mode promotion to the Specialists path.
// Other coordinator-tier agents are pre-existing specialised loops; they
// shouldn't be wrapped in Plan mode just because they hit a recoverable
// failure. Tests with no subagentType (the bare engine path) also fall
// through unmodified — escalation requires explicit opt-in via the
// Specialists name.
func escalationEligible(lc *loopConfig) bool {
	return lc != nil && lc.subagentType == "specialists"
}

// budgetExceeded is the same gate Plan uses; ReAct consults it before
// escalating so we don't burn another planner LLM call just to fail
// again immediately.
func (c *ReActCoordinator) budgetExceeded() (bool, string) {
	if c.deps == nil || c.deps.Budget == nil {
		return false, ""
	}
	return c.deps.Budget.Exceeded()
}

func (c *ReActCoordinator) snapshotBudget() BudgetSnapshot {
	if c.deps == nil || c.deps.Budget == nil {
		return BudgetSnapshot{}
	}
	return c.deps.Budget.Snapshot()
}

// shouldEscalate decides whether a ReAct outcome is worth promoting. The
// policy is intentionally narrow:
//   - terminal reasons indicating LLM-side failure or contract violation
//     → escalate (the L3 / L2 work might recover under a structured plan)
//   - terminal reasons indicating user abort, prompt-too-long, or
//     genuinely "completed" → don't escalate
//   - one or more contract failures with NO submitted artifacts → escalate
//     (the task didn't deliver anything; try plan mode)
//
// The function is small + pure for testability.
func shouldEscalate(r subAgentLoopResult) bool {
	switch r.Terminal.Reason {
	case types.TerminalCompleted:
		// Completed cleanly — trust it. Escalate only if the loop
		// produced contract failures despite "completed" status, which
		// happens when SubmitTaskResult never passed.
		return len(r.ContractFailures) > 0
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		// User-side abort; respect the abort.
		return false
	case types.TerminalPromptTooLong:
		// Plan mode wouldn't fix this; same context, same problem.
		return false
	case types.TerminalMaxTurns:
		// Out of turns — plan mode might reach the goal more directly.
		return true
	case types.TerminalModelError, types.TerminalBlockingLimit:
		// Recoverable LLM-side failure; a structured plan might survive.
		return true
	}
	// Unknown / explicitly bad: escalate when contract failures exist.
	return len(r.ContractFailures) > 0
}

// buildReActEscalation translates the failed ReAct result into the
// EscalationContext shape the Planner consumes. We preserve everything
// the Plan run might want: artifacts already produced, budget already
// spent, the failure reason.
func buildReActEscalation(r subAgentLoopResult, budget BudgetSnapshot) *EscalationContext {
	reason := strings.TrimSpace(r.Terminal.Message)
	if reason == "" {
		reason = "react terminated without clean completion"
	}
	return &EscalationContext{
		FromMode:       CoordinatorModeReAct,
		Reason:         reason,
		Failures:       append([]string(nil), r.ContractFailures...),
		PriorArtifacts: append([]types.ArtifactRef(nil), r.SubmittedArtifacts...),
		BudgetSpent:    budget,
		EscalatedAt:    time.Now(),
	}
}
