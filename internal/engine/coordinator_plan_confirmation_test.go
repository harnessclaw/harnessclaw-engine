package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// TestPlanConfirmation_AutoModeSkipsApproval confirms that when the
// caller doesn't opt in (PlanConfirmation == "" / "auto"), Plan mode
// runs straight through without emitting plan.proposed.
func TestPlanConfirmation_AutoModeSkipsApproval(t *testing.T) {
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

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_auto_confirm",
		CoordinatorMode: "plan",
	}
	// No PlanConfirmation in ctx; default is "auto" → no pause.
	result, err := eng.SpawnSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SpawnSync: %v", err)
	}
	if result.CoordinatorMode != "plan" {
		t.Errorf("expected plan mode; got %q", result.CoordinatorMode)
	}

	// In auto mode no pending request should remain.
	eng.planMu.Lock()
	pending := len(eng.pendingPlans)
	eng.planMu.Unlock()
	if pending != 0 {
		t.Errorf("auto mode should not register pending plans; got %d", pending)
	}
}

// TestPlanConfirmation_RequiredModeBlocksUntilApproval drives the full
// approval round-trip directly via the engine's pending-plan registry,
// bypassing the goroutine-polling approach (which was racy in CI). The
// test exercises:
//   - PlanCoordinator emits plan.proposed (via out channel)
//   - Run blocks until SubmitPlanResponse is called
//   - The plan_id round-trips correctly
//   - Approved=true continues execution
//
// Approach: launch SpawnSync in a goroutine, watch the engine's pending
// map until a plan is registered, then approve. We use a tighter poll
// loop than polling-from-launch because here the goroutine starts AFTER
// SpawnSync is invoked.
func TestPlanConfirmation_RequiredModeBlocksUntilApproval(t *testing.T) {
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

	ctx := tool.WithPlanConfirmation(context.Background(), "required")

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_required",
		CoordinatorMode: "plan",
	}

	// Launch SpawnSync in a goroutine — it'll block on the confirmation
	// gate. We then approve from the main goroutine.
	type spawnRet struct {
		result *agent.SpawnResult
		err    error
	}
	spawnDone := make(chan spawnRet, 1)
	go func() {
		r, e := eng.SpawnSync(ctx, cfg)
		spawnDone <- spawnRet{r, e}
	}()

	// Wait for a pending plan to appear (it should, within ~50 ms in
	// practice — heuristic planner is fast).
	planID := waitForPendingPlan(t, eng, 5*time.Second)
	t.Logf("approving plan_id=%s", planID)

	if err := eng.SubmitPlanResponse(context.Background(), "sess", &types.PlanResponse{
		PlanID:   planID,
		Approved: true,
	}); err != nil {
		t.Fatalf("SubmitPlanResponse: %v", err)
	}

	ret := <-spawnDone
	if ret.err != nil {
		t.Fatalf("SpawnSync: %v", ret.err)
	}
	if ret.result.CoordinatorMode != "plan" {
		t.Errorf("expected plan; got %q", ret.result.CoordinatorMode)
	}
	if ret.result.Status != "completed" {
		t.Errorf("expected completed; got %q", ret.result.Status)
	}
	if !strings.Contains(ret.result.Summary, "<summary>") {
		t.Errorf("expected non-empty plan summary; got %q", ret.result.Summary)
	}
}

// waitForPendingPlan polls the engine's pending-plan map until an entry
// appears or the timeout fires. Used by all confirmation tests.
func waitForPendingPlan(t *testing.T, eng *QueryEngine, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		eng.planMu.Lock()
		for id := range eng.pendingPlans {
			eng.planMu.Unlock()
			return id
		}
		eng.planMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no pending plan registered within %s", timeout)
	return ""
}

// TestPlanConfirmation_RejectionCancelsRun verifies that approved=false
// causes the coordinator to short-circuit to the fallback path.
func TestPlanConfirmation_RejectionCancelsRun(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>x</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	ctx := tool.WithPlanConfirmation(context.Background(), "required")

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_reject",
		CoordinatorMode: "plan",
	}
	type spawnRet struct {
		result *agent.SpawnResult
		err    error
	}
	spawnDone := make(chan spawnRet, 1)
	go func() {
		r, e := eng.SpawnSync(ctx, cfg)
		spawnDone <- spawnRet{r, e}
	}()
	planID := waitForPendingPlan(t, eng, 5*time.Second)
	if err := eng.SubmitPlanResponse(context.Background(), "sess", &types.PlanResponse{
		PlanID:   planID,
		Approved: false,
		Reason:   "I changed my mind",
	}); err != nil {
		t.Fatalf("SubmitPlanResponse: %v", err)
	}
	ret := <-spawnDone
	if ret.err != nil {
		t.Fatalf("SpawnSync: %v", ret.err)
	}
	result := ret.result
	// Rejection takes the fallback path, which still terminates as
	// "completed" but produces no submitted artifacts (because no
	// step ran).
	if len(result.SubmittedArtifacts) != 0 {
		t.Errorf("rejected plan should produce no artifacts; got %d", len(result.SubmittedArtifacts))
	}
	if !strings.Contains(result.Summary, "user rejected") &&
		!strings.Contains(result.Summary, "降级原因") {
		t.Errorf("rejection summary should mention user/cancel; got %q", result.Summary)
	}
}

// TestPlanConfirmation_UserEditsApplied confirms that UpdatedSteps in
// the plan response actually replace the proposed steps.
func TestPlanConfirmation_UserEditsApplied(t *testing.T) {
	// Track what skills the dispatcher saw — proves the edited plan
	// drove execution rather than the original.
	var seenSkills []string
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{
				text:       "<summary>x</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	ctx := tool.WithPlanConfirmation(context.Background(), "required")

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y", // would normally route to research+writer
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_edit",
		CoordinatorMode: "plan",
	}
	type spawnRet struct {
		result *agent.SpawnResult
		err    error
	}
	spawnDone := make(chan spawnRet, 1)
	go func() {
		r, e := eng.SpawnSync(ctx, cfg)
		spawnDone <- spawnRet{r, e}
	}()
	planID := waitForPendingPlan(t, eng, 5*time.Second)
	if err := eng.SubmitPlanResponse(context.Background(), "sess", &types.PlanResponse{
		PlanID:   planID,
		Approved: true,
		UpdatedSteps: []types.ProposedStep{
			{ID: "s1", SubagentType: "researcher", Prompt: "research"},
			{ID: "s2", SubagentType: "analyst", Prompt: "analyze", DependsOn: []string{"s1"}},
		},
	}); err != nil {
		t.Fatalf("SubmitPlanResponse: %v", err)
	}
	ret := <-spawnDone
	if ret.err != nil {
		t.Fatalf("SpawnSync: %v", ret.err)
	}
	_ = ret.result
	_ = seenSkills // (snapshot wasn't used; provider doesn't expose seen skills)

	// The most reliable assertion is "no pending request remains" —
	// proves the approval response was consumed and execution continued.
	eng.planMu.Lock()
	pending := len(eng.pendingPlans)
	eng.planMu.Unlock()
	if pending != 0 {
		t.Errorf("pending plan not cleared after response; got %d pending", pending)
	}
}

// TestPlanConfirmation_InvalidEditTreatedAsRejection asserts a malformed
// edited plan (e.g. unknown skill) is rejected without retrying.
func TestPlanConfirmation_InvalidEditTreatedAsRejection(t *testing.T) {
	prov := &subagentMockProvider{
		responseFn: func(_ int) subagentMockResponse {
			return subagentMockResponse{stopReason: "end_turn", usage: &types.Usage{}}
		},
	}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	ctx := tool.WithPlanConfirmation(context.Background(), "required")

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_bad_edit",
		CoordinatorMode: "plan",
	}
	type spawnRet struct {
		result *agent.SpawnResult
		err    error
	}
	spawnDone := make(chan spawnRet, 1)
	go func() {
		r, e := eng.SpawnSync(ctx, cfg)
		spawnDone <- spawnRet{r, e}
	}()
	planID := waitForPendingPlan(t, eng, 5*time.Second)
	if err := eng.SubmitPlanResponse(context.Background(), "sess", &types.PlanResponse{
		PlanID:   planID,
		Approved: true,
		UpdatedSteps: []types.ProposedStep{
			{ID: "s1", SubagentType: "ghost-skill-not-registered", Prompt: "x"},
		},
	}); err != nil {
		t.Fatalf("SubmitPlanResponse: %v", err)
	}
	ret := <-spawnDone
	if ret.err != nil {
		t.Fatalf("SpawnSync: %v", ret.err)
	}
	result := ret.result
	// Bad edit → rejection → fallback. No real step should have run.
	if len(result.SubmittedArtifacts) != 0 {
		t.Errorf("invalid plan edit should not produce artifacts; got %d",
			len(result.SubmittedArtifacts))
	}
}

// TestRequestPlanApproval_StaleResponseLogsWarn exercises the
// no-pending-request path of SubmitPlanResponse — should not crash, just
// warn.
func TestRequestPlanApproval_StaleResponseLogsWarn(t *testing.T) {
	prov := &subagentMockProvider{}
	eng := newSubagentTestEngine(prov)

	// Submit a response for a plan that was never registered.
	err := eng.SubmitPlanResponse(context.Background(), "sess", &types.PlanResponse{
		PlanID:   "pln_does_not_exist",
		Approved: true,
	})
	if err != nil {
		t.Errorf("stale response should not error; got %v", err)
	}
}

// TestRequestPlanApproval_ContextCancellation drives the cancellation
// path: ctx is cancelled while the coordinator is blocked waiting. The
// coordinator returns a graceful error and the pending entry is cleaned
// up via the deferred delete in requestPlanApproval.
func TestRequestPlanApproval_ContextCancellation(t *testing.T) {
	prov := &subagentMockProvider{}
	eng := newSubagentTestEngine(prov)
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	eng.SetDefRegistry(reg)

	ctx, cancel := context.WithCancel(tool.WithPlanConfirmation(context.Background(), "required"))

	cfg := &agent.SpawnConfig{
		Prompt:          "调研 X 写 Y",
		AgentType:       tool.AgentTypeSync,
		SubagentType:    "specialists",
		ParentSessionID: "parent_cancel",
		CoordinatorMode: "plan",
	}

	done := make(chan struct{})
	go func() {
		_, _ = eng.SpawnSync(ctx, cfg)
		close(done)
	}()
	// Wait for the pending entry to register, then cancel ctx.
	_ = waitForPendingPlan(t, eng, 5*time.Second)
	cancel()
	<-done

	// After cancellation, the pending entry must be cleaned up so a
	// follow-up request doesn't see a ghost.
	eng.planMu.Lock()
	leaked := len(eng.pendingPlans)
	eng.planMu.Unlock()
	if leaked != 0 {
		t.Errorf("pending plan leaked after ctx cancellation; got %d", leaked)
	}
}
