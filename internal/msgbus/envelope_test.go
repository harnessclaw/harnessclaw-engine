package msgbus_test

import (
	"testing"
	"time"

	"harnessclaw-go/internal/msgbus"
)

func TestEnvelopeBasic(t *testing.T) {
	m := msgbus.AgentMessage{
		MsgID: "m-001", Kind: msgbus.KindTask, From: "scheduler", To: "queue:leaf",
		TaskID: "t-001", SessionID: "sess-X", Ts: time.Now(),
	}
	if m.Kind != "task" {
		t.Errorf("Kind serialization: want 'task', got %q", string(m.Kind))
	}
}

func TestKindAllValues(t *testing.T) {
	all := []msgbus.Kind{
		msgbus.KindLifecycle, msgbus.KindControl, msgbus.KindAgentMsg,
		msgbus.KindNotify, msgbus.KindTask, msgbus.KindResult,
	}
	if len(all) != 6 {
		t.Fatalf("want 6 kinds, got %d", len(all))
	}
}
