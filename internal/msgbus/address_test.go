package msgbus_test

import (
	"testing"

	"harnessclaw-go/internal/msgbus"
)

func TestAddressAgent(t *testing.T) {
	a := msgbus.AddrAgent("t-001")
	if string(a) != "agent:t-001" {
		t.Errorf("want agent:t-001, got %q", string(a))
	}
	tid, ok := a.AgentTaskID()
	if !ok || tid != "t-001" {
		t.Errorf("AgentTaskID(): got %q ok=%v", tid, ok)
	}
}

func TestAddressQueue(t *testing.T) {
	a := msgbus.AddrQueue("leaf")
	if string(a) != "queue:leaf" {
		t.Errorf("want queue:leaf, got %q", string(a))
	}
	name, ok := a.QueueName()
	if !ok || name != "leaf" {
		t.Errorf("QueueName(): got %q ok=%v", name, ok)
	}
}

func TestMsgStatusValues(t *testing.T) {
	all := []msgbus.MsgStatus{msgbus.MsgQueued, msgbus.MsgDelivered, msgbus.MsgAcked, msgbus.MsgFailed}
	if len(all) != 4 {
		t.Fatalf("want 4 statuses")
	}
}
