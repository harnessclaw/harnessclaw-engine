package engine

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// TestCoordinatorRegistry_BuiltinModesRegistered confirms that NewQueryEngine
// wires the default ReAct + Plan factories so a freshly-constructed engine
// can resolve both modes without further init.
func TestCoordinatorRegistry_BuiltinModesRegistered(t *testing.T) {
	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)

	cases := []struct {
		name     string
		input    CoordinatorMode
		wantMode CoordinatorMode
	}{
		{"explicit react", CoordinatorModeReAct, CoordinatorModeReAct},
		{"explicit plan", CoordinatorModePlan, CoordinatorModePlan},
		{"empty falls back to react", "", CoordinatorModeReAct},
		{"unknown falls back to react", "debate-not-yet", CoordinatorModeReAct},
	}

	deps := &SharedDeps{Logger: zap.NewNop()}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coord := r.Resolve(tc.input, deps)
			if coord == nil {
				t.Fatalf("Resolve(%q) returned nil", tc.input)
			}
			if coord.Mode() != tc.wantMode {
				t.Errorf("Resolve(%q).Mode() = %q, want %q", tc.input, coord.Mode(), tc.wantMode)
			}
		})
	}
}

// TestCoordinatorRegistry_Register lets tests inject a custom mode without
// touching the built-ins. Confirms the extension point works — this is the
// pattern future modes (Debate, Vote, Hierarchical) will use to land
// without modifying the registry source.
func TestCoordinatorRegistry_Register(t *testing.T) {
	r := newCoordinatorRegistry()
	registerBuiltinCoordinators(r)

	const customMode CoordinatorMode = "test-debate"
	r.Register(customMode, func(deps *SharedDeps) Coordinator {
		return &stubCoordinator{mode: customMode}
	})

	deps := &SharedDeps{Logger: zap.NewNop()}
	coord := r.Resolve(customMode, deps)
	if coord.Mode() != customMode {
		t.Errorf("custom mode not resolved: got %q", coord.Mode())
	}
}

// TestCoordinatorMode_IsKnown guards the exhaustive enum check used by
// telemetry and validation paths.
func TestCoordinatorMode_IsKnown(t *testing.T) {
	known := []CoordinatorMode{CoordinatorModeReAct, CoordinatorModePlan}
	for _, m := range known {
		if !m.IsKnown() {
			t.Errorf("%q expected IsKnown=true", m)
		}
	}
	for _, m := range []CoordinatorMode{"", "garbage", "rea"} {
		if m.IsKnown() {
			t.Errorf("%q expected IsKnown=false", m)
		}
	}
}

// stubCoordinator is a no-op test double; never runs in production code.
type stubCoordinator struct {
	mode CoordinatorMode
}

func (s *stubCoordinator) Mode() CoordinatorMode { return s.mode }
func (s *stubCoordinator) Run(
	_ context.Context,
	_ *session.Session,
	_ *loopConfig,
	_ chan<- types.EngineEvent,
) subAgentLoopResult {
	return subAgentLoopResult{}
}
