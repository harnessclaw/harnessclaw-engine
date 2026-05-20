package emitv2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolPayload_PhaseFields_MarshalsCorrectly(t *testing.T) {
	p := ToolPayload{
		Name:       "Bash",
		Phase:      PhasePlanning,
		PhaseHint:  "梳理执行思路",
		PhaseBytes: 1234,
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"phase":"planning"`,
		`"phase_hint":"梳理执行思路"`,
		`"phase_bytes":1234`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %s in %s", want, s)
		}
	}
}

func TestToolPayload_PhaseFields_OmitEmpty(t *testing.T) {
	p := ToolPayload{Name: "Read"}
	b, _ := json.Marshal(p)
	s := string(b)
	for _, unwanted := range []string{"phase", "phase_hint", "phase_bytes"} {
		if strings.Contains(s, `"`+unwanted+`"`) {
			t.Errorf("empty %s should be omitted; got %s", unwanted, s)
		}
	}
}
