package agent

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/tools"
)

func mustRegister(t *testing.T, reg *AgentDefinitionRegistry, def *AgentDefinition) {
	t.Helper()
	if err := reg.Register(def); err != nil {
		t.Fatalf("Register(%s): %v", def.Name, err)
	}
}

func TestAgentDefinitionRegistry_RegisterAndGet(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	def := &AgentDefinition{
		Name:        "test-agent",
		Description: "A test agent",
		AgentType:   tool.AgentTypeSync,
	}
	mustRegister(t, reg, def)

	got := reg.Get("test-agent")
	if got == nil {
		t.Fatal("expected to find agent definition")
	}
	if got.Description != "A test agent" {
		t.Errorf("expected description 'A test agent', got %q", got.Description)
	}
}

func TestAgentDefinitionRegistry_GetNil(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	if reg.Get("nonexistent") != nil {
		t.Error("expected nil for unknown agent")
	}
}

func TestAgentDefinitionRegistry_Overwrite(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	mustRegister(t, reg, &AgentDefinition{Name: "x", Description: "first"})
	mustRegister(t, reg, &AgentDefinition{Name: "x", Description: "second"})

	got := reg.Get("x")
	if got.Description != "second" {
		t.Errorf("expected overwritten description 'second', got %q", got.Description)
	}
}

func TestAgentDefinitionRegistry_All(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	mustRegister(t, reg, &AgentDefinition{Name: "a"})
	mustRegister(t, reg, &AgentDefinition{Name: "b"})

	all := reg.All()
	if len(all) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(all))
	}
}

func TestAgentDefinitionRegistry_Names(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	mustRegister(t, reg, &AgentDefinition{Name: "alpha"})
	mustRegister(t, reg, &AgentDefinition{Name: "beta"})

	names := reg.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d", len(names))
	}
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["alpha"] || !nameSet["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestAgentDefinitionRegistry_RegisterBuiltins(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()

	if reg.Get("plan") == nil {
		t.Error("expected 'plan' builtin")
	}
	if reg.Get("freelancer") == nil {
		t.Error("expected 'freelancer' builtin")
	}
	if reg.Get("plan_agent") == nil {
		t.Error("expected 'plan_agent' builtin")
	}
	if reg.Get("plan_executor_agent") == nil {
		t.Error("expected 'plan_executor_agent' builtin")
	}
}

func TestBrowserAgentDefinition(t *testing.T) {
	def := BrowserAgentDefinition()

	if def.Name != BrowserAgentName {
		t.Fatalf("Name = %q", def.Name)
	}
	if def.Tier != TierSubAgent {
		t.Fatalf("Tier = %q, want %q", def.Tier, TierSubAgent)
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

	reg := NewAgentDefinitionRegistry()
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

// --- Tier validation tests ---

func TestRegister_RejectsSubAgentWithoutOutputSchema(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	err := reg.Register(&AgentDefinition{
		Name:      "bad",
		Tier:      TierSubAgent,
		AgentType: tool.AgentTypeSync,
	})
	if err == nil {
		t.Fatal("expected error: TierSubAgent without OutputSchema")
	}
	if !strings.Contains(err.Error(), "OutputSchema") {
		t.Errorf("error should mention OutputSchema, got: %v", err)
	}
}

func TestRegister_RejectsSubAgentWithDispatchTool(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	err := reg.Register(&AgentDefinition{
		Name:         "bad",
		Tier:         TierSubAgent,
		AgentType:    tool.AgentTypeSync,
		OutputSchema: map[string]any{"type": "object"},
		AllowedTools: []string{"freelance"},
	})
	if err == nil {
		t.Fatal("expected error: TierSubAgent with coordinator")
	}
	if !strings.Contains(err.Error(), "dispatch") {
		t.Errorf("error should mention dispatch, got: %v", err)
	}
}

func TestRegister_AcceptsValidSubAgent(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	def := &AgentDefinition{
		Name:         "good-worker",
		Tier:         TierSubAgent,
		AgentType:    tool.AgentTypeSync,
		Description:  "writes drafts",
		Skills:       []string{"writing", "summarize"},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"draft": map[string]any{"type": "string"}}},
		Limitations:  []string{"不擅长长篇报告"},
		ExampleTasks: []string{"写一封商务邮件"},
		CostTier:     CostCheap,
	}
	if err := reg.Register(def); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Get("good-worker") == nil {
		t.Fatal("expected definition to be registered")
	}
}

func TestEffectiveTier_Default(t *testing.T) {
	d := &AgentDefinition{Name: "x"}
	if got := d.EffectiveTier(); got != TierCoordinator {
		t.Errorf("EffectiveTier on empty Tier = %q, want %q", got, TierCoordinator)
	}
}

// --- FindBySkill / ListForPlanner ---

func TestFindBySkill(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	mustRegister(t, reg, &AgentDefinition{
		Name: "writer", Tier: TierSubAgent, AgentType: tool.AgentTypeSync,
		Skills:       []string{"writing", "summarize"},
		OutputSchema: map[string]any{"type": "object"},
	})
	mustRegister(t, reg, &AgentDefinition{
		Name: "researcher", Tier: TierSubAgent, AgentType: tool.AgentTypeSync,
		Skills:       []string{"web_search", "summarize"},
		OutputSchema: map[string]any{"type": "object"},
	})

	got := reg.FindBySkill("summarize")
	if len(got) != 2 {
		t.Errorf("FindBySkill(summarize) = %d, want 2", len(got))
	}
	got = reg.FindBySkill("writing")
	if len(got) != 1 || got[0].Name != "writer" {
		t.Errorf("FindBySkill(writing) = %v, want writer only", got)
	}
	if len(reg.FindBySkill("nonexistent")) != 0 {
		t.Error("FindBySkill(nonexistent) should be empty")
	}
}

// --- RenderSubAgentContract ---

func TestRenderSubAgentContract_NilOrCoordinatorReturnsEmpty(t *testing.T) {
	if got := RenderSubAgentContract(nil); got != "" {
		t.Errorf("nil def: want empty, got %q", got)
	}
	coord := &AgentDefinition{Name: "c", AgentType: tool.AgentTypeSync}
	if got := RenderSubAgentContract(coord); got != "" {
		t.Errorf("coordinator def: want empty, got %q", got)
	}
}

func TestRenderSubAgentContract_RendersAllSections(t *testing.T) {
	def := &AgentDefinition{
		Name:      "leaf",
		Tier:      TierSubAgent,
		AgentType: tool.AgentTypeSync,
		Skills:    []string{"writing", "polishing"},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"draft"},
		},
		Limitations: []string{
			"不做事实核查",
			"不写代码",
		},
	}
	got := RenderSubAgentContract(def)

	// L3 sub-agent contract intentionally focuses on what's L3-specific:
	// no further dispatch + EscalateToPlanner exit. ArtifactWrite /
	// SubmitTaskResult / <summary> mechanics live in artifactsGuidance —
	// don't assert them here, that's the redundancy we just trimmed.
	for _, want := range []string{
		"<sub-agent-contract>",
		"</sub-agent-contract>",
		"L3 sub-agent",
		"escalate_to_planner",
		"writing / polishing", // skills joined
		"output_schema",
		"```json", // schema fenced code block
		"\"required\"",
		"不做事实核查",
		"不写代码",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("contract missing %q\nfull:\n%s", want, got)
		}
	}
	// Inverse assertion: redundancy guard. If somebody re-adds these
	// strings to <sub-agent-contract>, that means the duplication crept
	// back in — Final Text contract / ArtifactWrite mechanics already live
	// in artifactsGuidance.
	for _, mustNotHave := range []string{
		"submit_task_result", // belongs in artifactsGuidance, not here
		"ArtifactWrite",      // ditto
	} {
		if strings.Contains(got, mustNotHave) {
			t.Errorf("contract redundantly mentions %q (lives in artifactsGuidance)", mustNotHave)
		}
	}
}

func TestRenderSubAgentContract_OmitsEmptySections(t *testing.T) {
	// Minimal valid sub-agent: only OutputSchema. No skills, no limitations.
	def := &AgentDefinition{
		Name:         "minimal",
		Tier:         TierSubAgent,
		AgentType:    tool.AgentTypeSync,
		OutputSchema: map[string]any{"type": "object"},
	}
	got := RenderSubAgentContract(def)

	if strings.Contains(got, "能力标签") {
		t.Error("skills section should be omitted when Skills empty")
	}
	if strings.Contains(got, "你**不**做以下事") {
		t.Error("limitations section should be omitted when Limitations empty")
	}
	// Schema section must still appear.
	if !strings.Contains(got, "output_schema") {
		t.Error("schema section should always appear when OutputSchema present")
	}
}

func TestListForPlanner_ExcludesCoordinators(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	mustRegister(t, reg, &AgentDefinition{
		Name: "worker", Tier: TierSubAgent, AgentType: tool.AgentTypeSync,
		OutputSchema: map[string]any{"type": "object"},
	})
	mustRegister(t, reg, &AgentDefinition{
		Name: "coord", Tier: TierCoordinator, AgentType: tool.AgentTypeSync,
	})

	listing := reg.ListForPlanner()
	if len(listing) != 1 || listing[0].Name != "worker" {
		t.Errorf("ListForPlanner = %+v, want only worker", listing)
	}
}

func TestFreelancer_BuiltinRegistration(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	def := reg.Get("freelancer")
	if def == nil {
		t.Fatal("freelancer builtin not registered")
	}
	if def.EffectiveTier() != TierSubAgent {
		t.Errorf("Tier = %s, want sub_agent", def.EffectiveTier())
	}
	if len(def.OutputSchema) == 0 {
		t.Error("OutputSchema must be set for TierSubAgent")
	}
	if def.IsTeamMember {
		t.Error("freelancer must NOT be a team member (emma should not see it)")
	}
	// AllowedTools must contain the four skill self-management tools
	wantTools := []string{"search_skill", "load_skill", "unload_skill", "list_loaded_skills"}
	for _, w := range wantTools {
		found := false
		for _, a := range def.AllowedTools {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllowedTools missing %q", w)
		}
	}
	// Must NOT contain "skill" tool — spec calls this out explicitly
	for _, a := range def.AllowedTools {
		if a == "skill" {
			t.Error("freelancer AllowedTools should NOT contain Skill tool (use LoadSkill)")
		}
	}

	// Regression: InputSchema must NOT require `task` because Task tool
	// puts the task description in cfg.Prompt, not cfg.Inputs. Requiring
	// it here would break every freelancer dispatch that includes
	// candidate_skills (cfg.Inputs becomes non-empty → schema validates →
	// finds no `task` → fails). See bug "input schema validation failed
	// for freelancer: required field task is missing".
	if req, ok := def.InputSchema["required"]; ok {
		if arr, ok := req.([]string); ok {
			for _, r := range arr {
				if r == "task" {
					t.Errorf("InputSchema.required must NOT contain %q — task text "+
						"flows through cfg.Prompt not cfg.Inputs; this would break "+
						"every dispatch with candidate_skills", r)
				}
			}
		}
	}
	// Properties should still describe candidate_skills (for L2 to know
	// the maxItems constraint).
	if props, ok := def.InputSchema["properties"].(map[string]any); ok {
		if _, hasCS := props["candidate_skills"]; !hasCS {
			t.Error("InputSchema.properties should describe candidate_skills")
		}
	}
}
