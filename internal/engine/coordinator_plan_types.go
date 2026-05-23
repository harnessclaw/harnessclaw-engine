package engine

import (
	"errors"
	"fmt"
	"sort"

	"harnessclaw-go/pkg/types"
)

// Plan is the DAG produced by a Planner and consumed by the Scheduler.
// Steps are ordered topologically when first emitted (Planner contract);
// runtime mutation should go through MarkResolved / TopologicalOrder so
// invariants (no cycles, every dep references a real step) survive.
type Plan struct {
	// Goal is the original task description, unmodified. Carried through so
	// review_goal can compare actual outputs against the user's intent
	// without having to thread the task all the way through the scheduler.
	Goal string

	// Steps is the ordered list of work units. ID is unique within a Plan;
	// DependsOn references prior steps' IDs.
	Steps []*PlanStep
}

// PlanStep is one work unit. The SubagentType field selects which L3
// sub-agent definition handles the step at dispatch time — same
// convention as agent.SpawnConfig.SubagentType.
//
// SubagentType is OPTIONAL: when empty, the Scheduler asks SkillResolver
// to pick an L3 based on the step's description / prompt at the moment
// of dispatch. The "decide who executes at execution time" model lets
// Plan focus on "what to do" while keeping "who does it" as a runtime
// concern. Planners that DO have strong opinions (LLM Planner with
// agent-aware prompts) can still pre-fill SubagentType.
type PlanStep struct {
	// ID identifies the step within its Plan. Format is implementation-
	// defined; "step_<n>" by the simple Planner. Must be unique within
	// the Plan.
	ID string

	// SubagentType names the L3 sub-agent definition that will run this
	// step. Matches AgentDefinition.Name (e.g. "writer" / "researcher").
	// Optional — see struct doc above. Was named "skill" before v1.16;
	// renamed to remove ambiguity with AgentDefinition.Skills (which is
	// the L3's capability tag list, a different concept).
	SubagentType string

	// Description is a short human-readable label for telemetry.
	Description string

	// DependsOn is the list of PlanStep IDs whose results this step needs
	// before it can run. Must reference IDs earlier in the Steps slice
	// (a forward dependency = bug; Validate catches it).
	DependsOn []string

	// Prompt is the natural-language seed handed to the L3 sub-agent's
	// LLM. The Scheduler may rewrite it to inline upstream artifacts
	// before dispatch.
	Prompt string

	// ExpectedOutputs declares what artifacts this step must produce.
	// Empty means "no hard contract"; non-empty enforces M3+M4
	// validation in the L3 driver.
	ExpectedOutputs []types.ExpectedOutput
}

// Validate runs structural checks: unique IDs, no forward / dangling
// deps, no obvious cycles. Run before the Scheduler picks up a Plan so
// later code can assume well-formed input.
//
// Cycle detection here is the simple "no DependsOn references a later
// ID in Steps" rule. That's a sufficient condition because Steps is
// expected to be in topological order; a cycle would manifest as a
// forward reference.
func (p *Plan) Validate() error {
	if p == nil {
		return errors.New("plan: nil")
	}
	if len(p.Steps) == 0 {
		return errors.New("plan: must have at least one step")
	}
	seen := make(map[string]int, len(p.Steps))
	for idx, step := range p.Steps {
		if step == nil {
			return fmt.Errorf("plan: step at index %d is nil", idx)
		}
		if step.ID == "" {
			return fmt.Errorf("plan: step at index %d has empty ID", idx)
		}
		// SubagentType may be empty: the Scheduler resolves it via
		// SkillResolver at dispatch time. We don't fail Validate here;
		// the resolution failure surfaces later as a step-level error.
		if _, dup := seen[step.ID]; dup {
			return fmt.Errorf("plan: duplicate step ID %q", step.ID)
		}
		seen[step.ID] = idx
		for _, dep := range step.DependsOn {
			depIdx, ok := seen[dep]
			if !ok {
				return fmt.Errorf("plan: step %q depends on unknown / forward step %q", step.ID, dep)
			}
			if depIdx >= idx {
				return fmt.Errorf("plan: step %q depends on later step %q (cycle or out-of-order)", step.ID, dep)
			}
		}
	}
	return nil
}

// TopologicalOrder returns step IDs in dependency-respecting order. When
// the Plan was produced by a well-behaved Planner, this matches the
// Steps slice order one-for-one — but external callers shouldn't rely on
// that, hence the explicit accessor.
func (p *Plan) TopologicalOrder() []string {
	order := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		order = append(order, s.ID)
	}
	return order
}

// Find returns the step with the given ID, or nil.
func (p *Plan) Find(id string) *PlanStep {
	for _, s := range p.Steps {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Sorted returns a new []*PlanStep sorted by ID — stable for snapshot
// comparisons in tests. Original Steps slice is not mutated.
func (p *Plan) Sorted() []*PlanStep {
	out := append([]*PlanStep(nil), p.Steps...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// StepResult captures what a Scheduler obtained when it ran one step.
// Carried through to subsequent steps (so step B can see step A's
// produced artifact IDs in its prompt) and aggregated into the final
// ResultEnvelope-equivalent.
type StepResult struct {
	StepID    string
	Status    string // "success" | "failed" | "skipped"
	Summary   string
	Artifacts []types.ArtifactRef
	Failures  []string
	Usage     *types.Usage
	// Attempts counts how many times the Scheduler tried this step
	// before recording a final result. 1 means "succeeded on first try"
	// (or failed once). > 1 means a transient failure was retried.
	// Surfaced on emit step.dispatched / step.completed / step.failed
	// so observers can see the retry happened.
	Attempts int
}
