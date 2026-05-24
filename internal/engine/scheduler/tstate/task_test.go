package tstate_test

import (
	"encoding/json"
	"testing"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestTaskStateJSONRoundtrip(t *testing.T) {
	ts := tstate.TaskState{
		ID:       "t-001",
		SessionID: "sess-X",
		Kind:     types.KindReact,
		Status:   types.StatusReady,
		Attempt:  0,
		LeafSpec: spec.TaskSpec{Goal: "read README"},
	}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	var got tstate.TaskState
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-001" || got.LeafSpec.Goal != "read README" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}
