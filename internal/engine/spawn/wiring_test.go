// internal/engine/spawn/wiring_test.go
package spawn

import (
	"testing"
)

// TestNewSpawner_Wiring asserts that constructing a Spawner with a
// fully-populated Deps does not panic and the Spawner is non-nil.
// Catches regressions where a new Deps method is added but its
// corresponding QueryEngine implementation is left unwired (which
// would compile fine but return a nil value at runtime).
func TestNewSpawner_Wiring(t *testing.T) {
	deps := newFakeDeps()
	s := NewSpawner(deps)
	if s == nil {
		t.Fatal("NewSpawner returned nil")
	}
}

// TestFakeDeps_SatisfiesInterface is a compile-time guard. If a new
// method is added to spawn.Deps but fakeDeps doesn't implement it,
// this line fails to compile.
var _ Deps = (*fakeDeps)(nil)
