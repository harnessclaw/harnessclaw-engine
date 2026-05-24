package types_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestTaskIDStringer(t *testing.T) {
	id := types.TaskID("t-001")
	if string(id) != "t-001" {
		t.Fatalf("want t-001, got %q", string(id))
	}
}

func TestMetaRefDistinctFromRef(t *testing.T) {
	var r types.Ref = "blob://abc"
	var m types.MetaRef = "meta.json"
	if string(r) == string(m) {
		t.Fatal("Ref and MetaRef are distinct types")
	}
}
