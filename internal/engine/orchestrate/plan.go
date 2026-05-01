// Package orchestrate implements the Phase-2 multi-step task executor used by
// the Orchestrate tool. It owns plan parsing, dependency-graph validation, and
// parallel execution of sub-agent steps with automatic context propagation.
//
// Design reference: docs/design/architecture/layered-architecture.md (§九).
package orchestrate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxSteps caps how many steps a plan may contain. Anything beyond this is
// treated as a Planner mistake and rejected — emma will degrade to Phase 1.
const MaxSteps = 10

// MaxStepRetries is the per-step retry budget inside the executor.
// First attempt + 2 retries = up to 3 total invocations per step.
const MaxStepRetries = 2

// Step is one node in the dependency graph produced by the Planner.
type Step struct {
	StepID       string   `json:"step_id"`
	SubagentType string   `json:"subagent_type"`
	Task         string   `json:"task"`
	DependsOn    []string `json:"depends_on"`
}

// Plan is the structured output of the Planner agent.
type Plan struct {
	Steps []Step `json:"steps"`
}

// jsonBlockRe matches ```json ... ``` fenced code blocks. The Planner is
// instructed to wrap its JSON in such a fence; we extract the first match.
var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// firstObjectRe is a fallback that captures the first balanced-looking JSON
// object in raw text. Used when the planner forgets the code fence.
var firstObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// ParsePlan extracts and decodes a plan JSON from the planner's raw output.
// The planner is told to emit a ```json ... ``` block; we accept the fenced
// form first and fall back to the largest brace-delimited object.
func ParsePlan(raw string) (*Plan, error) {
	candidate := ""
	if m := jsonBlockRe.FindStringSubmatch(raw); len(m) > 1 {
		candidate = m[1]
	} else if m := firstObjectRe.FindString(raw); m != "" {
		candidate = m
	}
	if candidate == "" {
		return nil, fmt.Errorf("no JSON object found in planner output")
	}
	candidate = strings.TrimSpace(candidate)

	var plan Plan
	if err := json.Unmarshal([]byte(candidate), &plan); err != nil {
		return nil, fmt.Errorf("plan JSON unmarshal failed: %w", err)
	}
	return &plan, nil
}

// Validate checks that the plan is well-formed and executable.
//
// allowedAgents, when non-empty, restricts subagent_type values to a known
// roster (emma's team plus the built-in profiles). Pass nil/empty to skip
// the roster check during unit tests.
func (p *Plan) Validate(allowedAgents map[string]bool) error {
	if p == nil {
		return fmt.Errorf("plan is nil")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan must contain at least one step")
	}
	if len(p.Steps) > MaxSteps {
		return fmt.Errorf("plan exceeds %d steps (got %d)", MaxSteps, len(p.Steps))
	}

	seen := make(map[string]bool, len(p.Steps))
	for i, s := range p.Steps {
		if s.StepID == "" {
			return fmt.Errorf("step #%d: step_id is required", i)
		}
		if seen[s.StepID] {
			return fmt.Errorf("duplicate step_id %q", s.StepID)
		}
		seen[s.StepID] = true
		if strings.TrimSpace(s.Task) == "" {
			return fmt.Errorf("step %s: task is required", s.StepID)
		}
		if s.SubagentType == "" {
			return fmt.Errorf("step %s: subagent_type is required", s.StepID)
		}
		if len(allowedAgents) > 0 && !allowedAgents[s.SubagentType] {
			return fmt.Errorf("step %s: unknown subagent_type %q", s.StepID, s.SubagentType)
		}
	}

	for _, s := range p.Steps {
		for _, dep := range s.DependsOn {
			if dep == s.StepID {
				return fmt.Errorf("step %s depends on itself", s.StepID)
			}
			if !seen[dep] {
				return fmt.Errorf("step %s depends on unknown step %q", s.StepID, dep)
			}
		}
	}

	if err := detectCycle(p.Steps); err != nil {
		return err
	}
	return nil
}

// detectCycle runs Kahn's algorithm to confirm the dependency graph is a DAG.
func detectCycle(steps []Step) error {
	inDeg := make(map[string]int, len(steps))
	out := make(map[string][]string, len(steps))
	for _, s := range steps {
		if _, ok := inDeg[s.StepID]; !ok {
			inDeg[s.StepID] = 0
		}
		for _, dep := range s.DependsOn {
			inDeg[s.StepID]++
			out[dep] = append(out[dep], s.StepID)
		}
	}

	queue := make([]string, 0, len(steps))
	for id, d := range inDeg {
		if d == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visited++
		for _, n := range out[cur] {
			inDeg[n]--
			if inDeg[n] == 0 {
				queue = append(queue, n)
			}
		}
	}
	if visited != len(steps) {
		return fmt.Errorf("plan contains a dependency cycle")
	}
	return nil
}
