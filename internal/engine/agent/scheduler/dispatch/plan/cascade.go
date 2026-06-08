package plan

import (
	"sort"

	"harnessclaw-go/internal/legacy/workspace"
)

// CascadeSkip marks all transitive dependents of failedID as Cancelled in the
// supplied plan, but only when their current Status is Pending. Tasks that
// already started (Running / Done / Failed / Cancelled) are left alone.
//
// The traversal uses BFS over the reverse DependsOn graph; cycles are tolerated
// because we never re-enqueue an already-visited task. Returns the list of
// step IDs that were transitioned to Cancelled (sorted for determinism).
//
// Callers are responsible for persisting the mutated plan back to disk.
func CascadeSkip(plan *workspace.Plan, failedID string) []string {
	if plan == nil || len(plan.Tasks) == 0 {
		return nil
	}
	// Reverse-edge index: taskID → IDs that depend on it.
	reverse := make(map[string][]string, len(plan.Tasks))
	for id, t := range plan.Tasks {
		for _, dep := range t.DependsOn {
			reverse[dep] = append(reverse[dep], id)
		}
	}

	visited := map[string]bool{failedID: true}
	skipped := make([]string, 0)
	queue := append([]string{}, reverse[failedID]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true

		t, ok := plan.Tasks[cur]
		if !ok || t == nil {
			continue
		}
		// Only skip pending tasks. In-flight / completed work is preserved.
		if t.Status == workspace.StatusPending {
			t.Status = workspace.StatusCancelled
			skipped = append(skipped, cur)
		}
		// Continue traversal regardless of whether we just marked this one,
		// so chains of dependents survive a mid-graph in-flight task.
		queue = append(queue, reverse[cur]...)
	}

	sort.Strings(skipped)
	return skipped
}

// TopoOrder returns the task IDs in a topological order suitable for sequential
// dispatch. Ties are broken alphabetically for determinism. Returns an error
// if a cycle is detected.
func TopoOrder(plan *workspace.Plan) ([]string, error) {
	if plan == nil || len(plan.Tasks) == 0 {
		return nil, nil
	}
	indeg := make(map[string]int, len(plan.Tasks))
	for id, t := range plan.Tasks {
		indeg[id] = len(t.DependsOn)
	}

	// Kahn's algorithm. We pop the lexicographically smallest in-degree-0
	// node each round so the order is deterministic across runs.
	out := make([]string, 0, len(plan.Tasks))
	for len(indeg) > 0 {
		// collect ready (in-degree 0) IDs and sort.
		ready := make([]string, 0)
		for id, d := range indeg {
			if d == 0 {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return nil, &CycleError{Remaining: indeg}
		}
		sort.Strings(ready)
		for _, id := range ready {
			out = append(out, id)
			delete(indeg, id)
			// Decrement in-degree of every dependent.
			for otherID, t := range plan.Tasks {
				if _, still := indeg[otherID]; !still {
					continue
				}
				for _, dep := range t.DependsOn {
					if dep == id {
						indeg[otherID]--
					}
				}
			}
		}
	}
	return out, nil
}

// CycleError is returned by TopoOrder when the plan contains a dependency cycle.
type CycleError struct {
	Remaining map[string]int
}

func (e *CycleError) Error() string {
	return "plan: dependency cycle detected"
}
