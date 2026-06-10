package definition_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/definition"
)

func TestRegisterBuiltins_PlanAgentsDefined(t *testing.T) {
	r := definition.NewRegistry()
	r.RegisterBuiltins()

	for _, name := range []string{"plan_agent", "plan_executor_agent"} {
		def := r.Get(name)
		if def == nil {
			t.Errorf("agent %q not registered", name)
			continue
		}
		if len(def.AllowedTools) == 0 {
			t.Errorf("agent %q has empty AllowedTools", name)
		}
	}

	// plan_agent must have plan_update but NOT freelance
	pa := r.Get("plan_agent")
	if pa != nil {
		hasPlanUpdate := false
		hasFreelance := false
		for _, tool := range pa.AllowedTools {
			if tool == "plan_update" {
				hasPlanUpdate = true
			}
			if tool == "freelance" {
				hasFreelance = true
			}
		}
		if !hasPlanUpdate {
			t.Error("plan_agent missing plan_update")
		}
		if hasFreelance {
			t.Error("plan_agent should NOT have freelance")
		}
	}

	// plan_executor_agent must have plan_read, plan_update, and freelance
	pea := r.Get("plan_executor_agent")
	if pea != nil {
		required := []string{"plan_read", "plan_update", "freelance"}
		for _, req := range required {
			found := false
			for _, tool := range pea.AllowedTools {
				if tool == req {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("plan_executor_agent missing %q", req)
			}
		}
	}
}
