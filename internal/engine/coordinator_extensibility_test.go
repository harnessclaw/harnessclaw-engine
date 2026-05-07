package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// TestExtensibility_RegisterCustomMode validates the design claim that
// adding a new mode (Phase E from the architecture roadmap) requires
// only Register on the registry plus an interface implementation — no
// changes to SpawnSync, no changes to SharedDeps, no changes to the
// coordinator routing code.
//
// The mock VoteCoordinator is a stand-in for a hypothetical future mode
// (Vote / Debate / Hierarchical). The test asserts:
//  1. Register accepts a brand-new mode name
//  2. Resolve picks the new mode when the explicit preference matches
//  3. Resolve still falls back to ReAct when the new mode is unknown
//     (i.e. registration order doesn't break the default policy)
//  4. The coordinator's Run is actually invoked when the registry routes
//     a spawn to it
func TestExtensibility_RegisterCustomMode(t *testing.T) {
	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)

	const voteMode CoordinatorMode = "vote"

	var ranCount int32
	r.Register(voteMode, func(deps *SharedDeps) Coordinator {
		return &voteCoordinator{mode: voteMode, deps: deps, ranCount: &ranCount}
	})

	deps := &SharedDeps{Logger: zap.NewNop()}

	// Preference matches the new mode → returns the custom coordinator.
	coord := r.Resolve(voteMode, deps)
	if coord.Mode() != voteMode {
		t.Fatalf("custom mode not resolved; got %q", coord.Mode())
	}

	// Run actually executes (the count flips).
	coord.Run(context.Background(), &session.Session{ID: "x"}, &loopConfig{}, nil)
	if atomic.LoadInt32(&ranCount) != 1 {
		t.Errorf("custom coordinator Run was not invoked; count=%d", ranCount)
	}

	// Unknown mode (different from voteMode and from the builtins)
	// still degrades to ReAct.
	fallback := r.Resolve(CoordinatorMode("not-registered"), deps)
	if fallback.Mode() != CoordinatorModeReAct {
		t.Errorf("unknown mode should fall back to react; got %q", fallback.Mode())
	}
}

// voteCoordinator stands in for any future custom mode (Vote / Debate /
// Hierarchical / ...). Mode is parameterized so a single test type can
// register multiple distinct modes; ranCount confirms Run was invoked.
type voteCoordinator struct {
	mode     CoordinatorMode
	deps     *SharedDeps
	ranCount *int32
}

func (c *voteCoordinator) Mode() CoordinatorMode {
	if c.mode == "" {
		return CoordinatorMode("vote")
	}
	return c.mode
}

func (c *voteCoordinator) Run(
	_ context.Context,
	_ *session.Session,
	_ *loopConfig,
	_ chan<- types.EngineEvent,
) subAgentLoopResult {
	if c.ranCount != nil {
		atomic.AddInt32(c.ranCount, 1)
	}
	return subAgentLoopResult{
		Terminal: types.Terminal{
			Reason:  types.TerminalCompleted,
			Message: "custom-mode stub executed",
		},
	}
}

// TestExtensibility_RegisterOverwritesExistingMode pins the contract that
// Register replaces an existing factory. This matters when tests want
// to swap in a fake (e.g. observer for a builtin mode without changing
// production code).
func TestExtensibility_RegisterOverwritesExistingMode(t *testing.T) {
	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)

	var observerInvoked int32
	r.Register(CoordinatorModeReAct, func(deps *SharedDeps) Coordinator {
		return &voteCoordinator{mode: CoordinatorModeReAct, deps: deps, ranCount: &observerInvoked}
	})

	deps := &SharedDeps{Logger: zap.NewNop()}
	coord := r.Resolve(CoordinatorModeReAct, deps)
	coord.Run(context.Background(), &session.Session{ID: "x"}, &loopConfig{}, nil)
	if atomic.LoadInt32(&observerInvoked) != 1 {
		t.Error("test override of ReAct factory was not applied")
	}
}

// TestExtensibility_HeuristicSelectorIgnoresUnknownMode confirms that
// the B-mode selector only ever returns known modes. Future modes the
// selector doesn't yet know about must be opt-in via ExplicitMode; the
// auto-decision path stays conservative on (react, plan).
func TestExtensibility_HeuristicSelectorIgnoresUnknownMode(t *testing.T) {
	s := NewHeuristicModeSelector()
	out := s.Select(context.Background(), ModeSelectorInput{
		Goal:         "调研 X 写 Y",
		ExplicitMode: CoordinatorMode("vote"),
	})
	if out.Mode == CoordinatorMode("vote") {
		t.Error("heuristic selector must not echo unknown explicit modes")
	}
	if !out.Mode.IsKnown() {
		t.Errorf("selector returned unknown mode %q", out.Mode)
	}
}

// TestExtensibility_NewModeDoesNotBreakResolutionPolicy is a smoke test
// that adding a third mode doesn't accidentally make the registry forget
// how to fall back to ReAct.
func TestExtensibility_NewModeDoesNotBreakResolutionPolicy(t *testing.T) {
	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)
	r.Register(CoordinatorMode("debate"), func(deps *SharedDeps) Coordinator {
		return &voteCoordinator{mode: CoordinatorMode("debate"), deps: deps, ranCount: new(int32)}
	})

	deps := &SharedDeps{Logger: zap.NewNop()}
	cases := []struct {
		input    CoordinatorMode
		wantMode CoordinatorMode
	}{
		{"react", CoordinatorModeReAct},
		{"plan", CoordinatorModePlan},
		{"debate", CoordinatorMode("debate")},
		{"unknown", CoordinatorModeReAct},
		{"", CoordinatorModeReAct},
	}
	for _, c := range cases {
		got := r.Resolve(c.input, deps).Mode()
		if got != c.wantMode {
			t.Errorf("resolve(%q) = %q, want %q", c.input, got, c.wantMode)
		}
	}
}

// TestExtensibility_DocumentedExtensionPath proves the actual lines of
// code needed to add a mode are minimal — a sanity check against
// architecture documentation.
func TestExtensibility_DocumentedExtensionPath(t *testing.T) {
	// All it takes:
	//   1. define a new CoordinatorMode constant
	//   2. implement Coordinator (Mode + Run)
	//   3. r.Register(modeConst, factory)

	const myMode CoordinatorMode = "custom-test-mode"
	deps := &SharedDeps{Logger: zap.NewNop()}

	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)
	r.Register(myMode, func(d *SharedDeps) Coordinator {
		return &voteCoordinator{mode: myMode, deps: d, ranCount: new(int32)}
	})

	got := r.Resolve(myMode, deps).Mode()
	if string(got) != string(myMode) {
		t.Fatalf("custom mode not resolved; got %q", got)
	}

	// And the registration is local — the original mode set still works:
	if r.Resolve("plan", deps).Mode() != CoordinatorModePlan {
		t.Error("registering a new mode should not break existing modes")
	}
	if !strings.HasPrefix(string(myMode), "custom-") {
		t.Error("sanity")
	}
}
