package spec_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestTaskSpecDepRef(t *testing.T) {
	sp := spec.TaskSpec{
		LocalID: "s2",
		Goal:    "design API",
		Hint:    spec.Hint{Kind: types.KindReact},
		Deps:    []spec.DepRef{"s1"},
	}
	if sp.LocalID != "s2" {
		t.Errorf("LocalID lost")
	}
	if len(sp.Deps) != 1 || sp.Deps[0] != spec.DepRef("s1") {
		t.Errorf("Deps lost")
	}
}

func TestTaskSpecZeroValueSafe(t *testing.T) {
	var sp spec.TaskSpec
	if sp.LocalID != "" || sp.Goal != "" || len(sp.Deps) != 0 {
		t.Fatal("zero value should be empty")
	}
}

func TestTaskSpecSessionID(t *testing.T) {
	sp := spec.TaskSpec{SessionID: "sess-X", Goal: "x"}
	if sp.SessionID != "sess-X" {
		t.Fatal("SessionID lost")
	}
}
