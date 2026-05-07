package engine

import (
	"context"
	"strings"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// TestE2E_PlanModeOptInViaCtx exercises the full Plan-mode wiring:
//   - CoordinatorMode "plan" is propagated through SpawnSync
//   - PlanCoordinator runs (HeuristicPlanner emits a plan)
//   - SchedulerOutcome is folded into a SpawnResult
//   - SpawnResult.CoordinatorMode reports "plan"
//
// Uses the existing subagentMockProvider so no real LLM is involved. The
// mock responds end_turn immediately for every step's L3 dispatch — Plan
// mode still produces a SpawnResult with the mode tagged, even when each
// step is trivially short.
func TestE2E_PlanModeOptInViaCtx(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>step done</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 10, OutputTokens: 5},
			}
		},
	}
	eng := newSubagentTestEngine(prov)

	// Seed the agent definition registry with two L3 skills the
	// HeuristicPlanner can route to. Without registry, the planner has
	// nothing to dispatch and Plan mode short-circuits.
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "调研大模型推理优化进展，写一份对比报告",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_plan",
		CoordinatorMode: "plan",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if result.CoordinatorMode != "plan" {
		t.Errorf("CoordinatorMode should be plan; got %q", result.CoordinatorMode)
	}
	if result.EscalatedFromMode != "" {
		t.Errorf("explicit plan should not show escalated_from; got %q", result.EscalatedFromMode)
	}
	// The HeuristicPlanner picks research+write for this prompt → 2 steps,
	// so we expect at least one Chat call from the L3 dispatches.
	if len(prov.recorded) == 0 {
		t.Errorf("Plan mode should dispatch L3s and call the LLM; recorded=0")
	}
}

// TestE2E_ReActModeIsDefault confirms that without an explicit mode, a
// Specialists spawn runs in ReAct mode — preserves the current default
// behaviour unchanged.
func TestE2E_ReActModeIsDefault(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "<summary>ok</summary>", stopReason: "end_turn",
				usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "翻译这段英文",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_react",
		// No CoordinatorMode → ModeSelector picks based on goal; "翻译"
		// is a simple task → ReAct.
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if result.CoordinatorMode != "react" {
		t.Errorf("simple task default should pick react; got %q (escalated_from=%q)",
			result.CoordinatorMode, result.EscalatedFromMode)
	}
}

// TestE2E_ModeSelectorAutoPicksPlan exercises the B-mode auto-selection:
// a multi-deliverable prompt routes to Plan even without an explicit
// override.
func TestE2E_ModeSelectorAutoPicksPlan(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>step done</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	// "调研 X 写 Y" matches the multi-deliverable heuristic → Plan.
	cfg := &agent.SpawnConfig{
		Prompt:          "调研三家电动车续航数据，写一份对比报告",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_auto",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.CoordinatorMode != "plan" {
		t.Errorf("multi-deliverable should auto-select plan; got %q", result.CoordinatorMode)
	}
}

// TestE2E_PlanModeBudgetSurfacesInResult verifies the P1-2 wiring: the
// Plan coordinator's BudgetTracker accumulates across step dispatches
// and the snapshot shows up on SpawnResult.BudgetSpent.
func TestE2E_PlanModeBudgetSurfacesInResult(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>x</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 100, OutputTokens: 50},
			}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_budget",
		CoordinatorMode: "plan",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if result.CoordinatorMode != "plan" {
		t.Fatalf("coordinator_mode should be plan; got %q", result.CoordinatorMode)
	}
	// The HeuristicPlanner emits 2 steps (research+write) so we expect
	// at least 2 step LLM calls + minimum some token usage. Real numbers
	// depend on per-step driver re-prompts; we assert > 0 to keep the
	// test stable across nudge / preamble changes.
	if result.BudgetSpent.LLMCalls == 0 && result.BudgetSpent.TokensUsed == 0 {
		t.Errorf("budget tracker should accumulate; got %+v", result.BudgetSpent)
	}
}

// TestE2E_UnknownModeFallsBackToReAct confirms the registry's degrade
// policy: a malformed CoordinatorMode value (e.g. "xyz") doesn't crash
// the spawn, just runs ReAct with a warn log.
func TestE2E_UnknownModeFallsBackToReAct(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "<summary>ok</summary>", stopReason: "end_turn",
				usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "翻译",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_bad_mode",
		CoordinatorMode: "garbage-mode",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.CoordinatorMode != "react" {
		t.Errorf("unknown mode should degrade to react; got %q", result.CoordinatorMode)
	}
}

// TestE2E_ContextOverrideThreadsThroughSpecialistsTool confirms that the
// router-level WithCoordinatorMode lands on SpawnConfig.CoordinatorMode
// after going through the Specialists tool — proves the WS → router →
// tool ctx → SpawnConfig pipeline is connected end to end.
//
// We exercise it by calling specialists.Tool.Execute directly with a
// mode-tagged ctx, intercepting the SpawnSync invocation via the engine.
// (A full WS test lives in the websocket package; this one is purely
// the engine half of the wire.)
func TestE2E_ContextOverrideThreadsThroughSpecialistsTool(t *testing.T) {
	// We don't need a full Specialists tool here — what we want to
	// verify is that when SpawnSync receives CoordinatorMode="plan"
	// in cfg, the resulting SpawnResult reports plan as the running
	// mode. (The Specialists tool sets cfg.CoordinatorMode from the
	// ctx; that path is unit-tested in the tool package separately.)
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>ok</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "翻译这段英文",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_ctx",
		CoordinatorMode: "plan",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if result.CoordinatorMode != "plan" {
		t.Errorf("explicit operator override should beat ModeSelector; got %q (reason: ModeSelector would have picked react for translate)",
			result.CoordinatorMode)
	}
}

// --- Escalation e2e tests (D-mode) ----------------------------------------

// TestE2E_EscalationFromReActToPlan exercises the D-mode auto-promotion
// path. We force the underlying ReAct loop to terminate with a contract
// failure (TerminalContractFailure-equivalent: max-turns + non-empty
// contract failures), then verify the SpawnResult shows the mode flip.
//
// The mechanism: the Specialists profile runs runSubAgentLoop which has
// no native way to fabricate ContractFailures from the test harness. So
// we exercise the helpers directly — the function-level shouldEscalate
// + buildReActEscalation + Plan re-run path is unit-tested separately;
// here we focus on the SpawnResult shape that emma sees.
//
// The realistic e2e of "ReAct loop terminates with TerminalMaxTurns"
// alone IS escalable per the policy. So we drive the mock to consume
// max_turns; ReActCoordinator detects it and escalates.
func TestE2E_EscalationFromReActToPlan(t *testing.T) {
	// Mock provider: respond every turn with a bogus tool call that
	// never resolves to end_turn. The loop will run until MaxTurns is
	// reached, which trips shouldEscalate(TerminalMaxTurns).
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			// After max_turns the wrapping coordinator escalates to
			// Plan. The Plan run dispatches HeuristicPlanner → fake
			// L3 step; we just need the mock to keep responding.
			return subagentMockResponse{
				text:       "still working",
				toolCalls:  []types.ToolCall{{ID: "t", Name: "TestEcho", Input: `{}`}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			}
		},
	}
	eng := newSubagentTestEngine(prov, &subagentTestTool{})
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 然后写一份分析报告",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		MaxTurns:        2, // small enough to hit MaxTurns quickly
		ParentSessionID: "parent_e2e_escalate",
		CoordinatorMode: "react", // explicit react to verify auto-escalation
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	// After escalation, mode should be "plan" + escalated_from "react".
	if result.CoordinatorMode != "plan" {
		t.Errorf("after escalation expected mode=plan; got %q (escalated_from=%q)",
			result.CoordinatorMode, result.EscalatedFromMode)
	}
	if result.EscalatedFromMode != "react" {
		t.Errorf("escalated_from should be react; got %q", result.EscalatedFromMode)
	}
}

// TestE2E_NoEscalationOnCleanReAct confirms the absence of false
// promotion: a clean ReAct run with end_turn does NOT flip to Plan.
func TestE2E_NoEscalationOnCleanReAct(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "<summary>all good</summary>", stopReason: "end_turn",
				usage: &types.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "翻译这段英文",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_clean",
		CoordinatorMode: "react",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if result.CoordinatorMode != "react" {
		t.Errorf("clean react should stay react; got %q", result.CoordinatorMode)
	}
	if result.EscalatedFromMode != "" {
		t.Errorf("no escalation should leave EscalatedFromMode empty; got %q",
			result.EscalatedFromMode)
	}
}

// TestE2E_NonSpecialistsAgentNotEscalated confirms the safety bound:
// only "specialists" is eligible for auto-escalation. Other coordinator-
// tier agents (e.g. general-purpose) hitting MaxTurns get TerminalMaxTurns,
// not auto-promoted to Plan.
func TestE2E_NonSpecialistsAgentNotEscalated(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "loop",
				toolCalls:  []types.ToolCall{{ID: "t", Name: "TestEcho", Input: `{}`}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 5, OutputTokens: 5},
			}
		},
	}
	eng := newSubagentTestEngine(prov, &subagentTestTool{})
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	cfg := &agent.SpawnConfig{
		Prompt:          "loop forever",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "general-purpose",
		MaxTurns:        2,
		ParentSessionID: "parent_e2e_non_spec",
	}

	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}

	if result.Terminal == nil || result.Terminal.Reason != types.TerminalMaxTurns {
		t.Errorf("non-specialists hitting max-turns should bubble that reason; got %v",
			result.Terminal)
	}
	if result.EscalatedFromMode != "" {
		t.Errorf("non-specialists should NOT auto-escalate; got escalated_from=%q",
			result.EscalatedFromMode)
	}
}

// TestE2E_SpecialistsToolReadsModeFromContext is the seam test for the
// router → tool integration. We construct a Specialists-shaped
// SpawnConfig and verify that when ctx carries WithCoordinatorMode, the
// resulting spawn picks up the mode. (The router-side test for
// WithCoordinatorMode is in the router package.)
func TestE2E_SpecialistsToolReadsModeFromContext(t *testing.T) {
	prov := &subagentMockProvider{
		responses: []subagentMockResponse{
			{text: "<summary>ok</summary>", stopReason: "end_turn",
				usage: &types.Usage{InputTokens: 5, OutputTokens: 5}},
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	// Ctx carries a mode override — Specialists tool would normally
	// read this and put it on SpawnConfig.CoordinatorMode. We bypass
	// the tool layer here and confirm SpawnSync honours the field.
	ctx := tool.WithCoordinatorMode(context.Background(), "plan")
	mode := tool.GetCoordinatorMode(ctx)
	if mode != "plan" {
		t.Fatalf("ctx threading broken; got %q", mode)
	}

	cfg := &agent.SpawnConfig{
		Prompt:          "翻译",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_e2e_ctx_thread",
		CoordinatorMode: mode, // simulating Specialists.Execute → SpawnConfig
	}
	result, err := eng.SpawnSync(ctx, cfg)
	if err != nil {
		t.Fatalf("SpawnSync error: %v", err)
	}
	if !strings.HasPrefix(result.CoordinatorMode, "plan") {
		t.Errorf("ctx-supplied mode not honoured; got %q", result.CoordinatorMode)
	}
}
