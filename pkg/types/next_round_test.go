package types_test

import (
	"testing"

	"harnessclaw-go/pkg/types"
)

// True position-correctness of NextRoundThinking emission is verified
// by the translator's M4 test (T10 in the spec test list). This file
// is a guard that the event-type constant doesn't get accidentally
// removed by future refactors.
func TestNextRoundThinking_EmissionPointExists(t *testing.T) {
	if types.EngineEventNextRoundThinking == "" {
		t.Fatal("EngineEventNextRoundThinking must be defined")
	}
}
