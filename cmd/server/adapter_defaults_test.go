package main

import "testing"

// TestResolveEffectiveTemperature covers the agent.temperature → vendor
// range scaling rules:
//
//   - openai / gemini:  agent value ×2 (target range [0, 2])
//   - anthropic:        agent value ×1 (target range [0, 1])
//   - unknown type:     agent value passes through unscaled
//   - agent value 0:    fall back to endpoint's own value
func TestResolveEffectiveTemperature(t *testing.T) {
	cases := []struct {
		name       string
		provType   string
		agentTemp  float64
		epTemp     float64
		want       float64
	}{
		{"anthropic_uses_agent_x1", "anthropic", 0.5, 0.2, 0.5},
		{"openai_scales_agent_x2", "openai", 0.5, 0.2, 1.0},
		{"gemini_scales_agent_x2", "gemini", 0.7, 0.2, 1.4},
		{"agent_zero_falls_back_to_endpoint", "openai", 0, 0.3, 0.3},
		{"both_zero_returns_zero", "openai", 0, 0, 0},
		{"unknown_type_passes_through", "exotic", 0.5, 0, 0.5},
		{"agent_at_1_scales_to_2_for_openai", "openai", 1.0, 0, 2.0},
		{"agent_at_1_stays_1_for_anthropic", "anthropic", 1.0, 0, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveEffectiveTemperature(c.provType, c.agentTemp, c.epTemp)
			if got != c.want {
				t.Errorf("resolveEffectiveTemperature(%q, %v, %v) = %v, want %v",
					c.provType, c.agentTemp, c.epTemp, got, c.want)
			}
		})
	}
}

// TestResolveEffectiveMaxTokens covers agent.max_tokens vs endpoint cap:
//
//   - agent ≤ endpoint cap → agent wins
//   - agent > endpoint cap → endpoint wins (endpoint is hard ceiling)
//   - agent == 0           → endpoint wins
//   - endpoint == 0        → agent wins (no cap configured)
func TestResolveEffectiveMaxTokens(t *testing.T) {
	cases := []struct {
		name     string
		agentMax int
		epMax    int
		want     int
	}{
		{"agent_under_cap_wins", 4096, 8192, 4096},
		{"agent_equals_cap_wins", 8192, 8192, 8192},
		{"agent_over_cap_capped", 16384, 8192, 8192},
		{"agent_zero_uses_endpoint", 0, 4096, 4096},
		{"both_zero_returns_zero", 0, 0, 0},
		{"endpoint_zero_no_cap", 4096, 0, 4096},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveEffectiveMaxTokens(c.agentMax, c.epMax)
			if got != c.want {
				t.Errorf("resolveEffectiveMaxTokens(%d, %d) = %d, want %d",
					c.agentMax, c.epMax, got, c.want)
			}
		})
	}
}
