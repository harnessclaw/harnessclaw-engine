package types

import "testing"

func TestEngineEvent_PhaseEventTypes_Exist(t *testing.T) {
	cases := []struct {
		name string
		got  EngineEventType
		want string
	}{
		{"ToolPlanning", EngineEventToolPlanning, "tool_planning"},
		{"ToolPlanningProgress", EngineEventToolPlanningProgress, "tool_planning_progress"},
		{"ToolQueued", EngineEventToolQueued, "tool_queued"},
		{"ToolPlanningRetract", EngineEventToolPlanningRetract, "tool_planning_retract"},
		{"NextRoundThinking", EngineEventNextRoundThinking, "next_round_thinking"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestEngineEvent_BytesField(t *testing.T) {
	ev := EngineEvent{
		Type:      EngineEventToolPlanningProgress,
		ToolUseID: "toolu_1",
		Bytes:     1024,
	}
	if ev.Bytes != 1024 {
		t.Errorf("Bytes field not set, got %d", ev.Bytes)
	}
}
