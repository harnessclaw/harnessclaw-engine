package agenttool

import (
	"encoding/json"
	"testing"
)

// TestValidate_AcceptsTeamMemberSubAgentTypes confirms validate does NOT
// reject sub-agent types declared in agentToolDescription (writer /
// researcher / analyst / developer / lifestyle / scheduler). Prior to
// fix, the hardcoded whitelist rejected these, costing one wasted
// round-trip per Specialists dispatch that picked a team-member name.
func TestValidate_AcceptsTeamMemberSubAgentTypes(t *testing.T) {
	for _, st := range []string{
		"general-purpose", "Explore", "explore", "Plan", "plan",
		"writer", "researcher", "analyst", "developer", "lifestyle", "scheduler",
		"",           // empty defaults to general-purpose downstream
		"team_alpha", // arbitrary names — registry decides at spawn time
	} {
		in := &agentInput{Prompt: "x", SubagentType: st}
		if err := in.validate(); err != nil {
			t.Errorf("validate rejected %q: %v", st, err)
		}
	}
}

// TestValidate_StillRejectsEmptyPrompt keeps the existing prompt
// requirement intact — relaxation is only on subagent_type.
func TestValidate_StillRejectsEmptyPrompt(t *testing.T) {
	in := &agentInput{Prompt: "", SubagentType: "general-purpose"}
	if err := in.validate(); err == nil {
		t.Error("validate must still reject empty prompt")
	}
}

func TestParseInput_CandidateSkills(t *testing.T) {
	raw := json.RawMessage(`{
		"prompt": "do x",
		"subagent_type": "freelancer",
		"candidate_skills": ["a", "b"]
	}`)
	in, err := parseInput(raw)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if len(in.CandidateSkills) != 2 || in.CandidateSkills[0] != "a" {
		t.Errorf("CandidateSkills = %v", in.CandidateSkills)
	}
}
