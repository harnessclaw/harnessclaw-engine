package common_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/pkg/types"
)

func TestEmitSubagentStart_IncludesParentIDs(t *testing.T) {
	out := make(chan types.EngineEvent, 4)
	common.EmitSubagentStart(out, common.StartEvent{
		AgentID:         "sub_aaa",
		AgentName:       "freelancer",
		AgentDesc:       "test",
		AgentTask:       "do x",
		SubagentType:    "freelancer",
		ParentAgentID:   "main",
		ParentSessionID: "sess_user",
		ParentStepID:    "step_1",
	})
	close(out)

	var got *types.EngineEvent
	for ev := range out {
		ev := ev
		got = &ev
	}
	if got == nil {
		t.Fatal("no event emitted")
	}
	if got.Type != types.EngineEventSubAgentStart {
		t.Errorf("Type = %v, want SubAgentStart", got.Type)
	}
	if got.ParentAgentID != "main" {
		t.Errorf("ParentAgentID = %q, want main", got.ParentAgentID)
	}
	if got.ParentSessionID != "sess_user" {
		t.Errorf("ParentSessionID = %q, want sess_user", got.ParentSessionID)
	}
	if got.ParentStepID != "step_1" {
		t.Errorf("ParentStepID = %q, want step_1", got.ParentStepID)
	}
	if got.AgentID != "sub_aaa" || got.AgentName != "freelancer" {
		t.Errorf("AgentID/Name not propagated: id=%q name=%q", got.AgentID, got.AgentName)
	}
	if got.SubagentType != "freelancer" {
		t.Errorf("SubagentType = %q, want freelancer", got.SubagentType)
	}
}

func TestEmitSubagentEnd_IncludesParentIDs(t *testing.T) {
	out := make(chan types.EngineEvent, 4)
	common.EmitSubagentEnd(out, common.EndEvent{
		AgentID:         "sub_aaa",
		AgentName:       "freelancer",
		AgentStatus:     "completed",
		SubagentType:    "freelancer",
		DurationMs:      123,
		ParentAgentID:   "main",
		ParentSessionID: "sess_user",
	})
	close(out)

	var got *types.EngineEvent
	for ev := range out {
		ev := ev
		got = &ev
	}
	if got == nil {
		t.Fatal("no event emitted")
	}
	if got.Type != types.EngineEventSubAgentEnd {
		t.Errorf("Type = %v, want SubAgentEnd", got.Type)
	}
	if got.AgentStatus != "completed" {
		t.Errorf("AgentStatus = %q, want completed", got.AgentStatus)
	}
	if got.Duration != 123 {
		t.Errorf("Duration = %d, want 123", got.Duration)
	}
	if got.ParentAgentID != "main" {
		t.Errorf("ParentAgentID = %q, want main", got.ParentAgentID)
	}
	if got.ParentSessionID != "sess_user" {
		t.Errorf("ParentSessionID = %q, want sess_user", got.ParentSessionID)
	}
}

func TestEmitSubagent_NilChannelNoop(t *testing.T) {
	// Must not panic.
	common.EmitSubagentStart(nil, common.StartEvent{AgentID: "x"})
	common.EmitSubagentEnd(nil, common.EndEvent{AgentID: "x"})
}

func TestEmitSubagent_FullChannelDrops(t *testing.T) {
	out := make(chan types.EngineEvent) // unbuffered, no receiver -> drop
	// Must not block.
	common.EmitSubagentStart(out, common.StartEvent{AgentID: "x"})
	common.EmitSubagentEnd(out, common.EndEvent{AgentID: "x"})
}

func TestBuildSpawnResult_PopulatesFields(t *testing.T) {
	term := types.Terminal{Reason: types.TerminalCompleted, Turn: 3}
	usage := types.Usage{InputTokens: 100, OutputTokens: 50}
	res := common.BuildSpawnResult("sess_x", "agent_x", "hello world", term, usage, 5)
	if res == nil {
		t.Fatal("BuildSpawnResult returned nil")
	}
	if res.SessionID != "sess_x" {
		t.Errorf("SessionID = %q, want sess_x", res.SessionID)
	}
	if res.AgentID != "agent_x" {
		t.Errorf("AgentID = %q, want agent_x", res.AgentID)
	}
	if res.Output != "hello world" {
		t.Errorf("Output = %q, want hello world", res.Output)
	}
	if res.NumTurns != 5 {
		t.Errorf("NumTurns = %d, want 5", res.NumTurns)
	}
	if res.Terminal == nil || res.Terminal.Reason != types.TerminalCompleted {
		t.Errorf("Terminal not propagated: %+v", res.Terminal)
	}
	if res.Usage == nil || res.Usage.InputTokens != 100 {
		t.Errorf("Usage not propagated: %+v", res.Usage)
	}
}
