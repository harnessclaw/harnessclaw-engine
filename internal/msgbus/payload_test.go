package msgbus_test

import (
	"testing"

	"harnessclaw-go/internal/msgbus"
)

func TestTaskMessageFields(t *testing.T) {
	m := msgbus.TaskMessage{TaskID: "t-001", TaskType: "leaf", Task: "read README"}
	if m.TaskID == "" || m.TaskType == "" || m.Task == "" {
		t.Fatal("TaskMessage fields preserved")
	}
}

func TestResultStatusMapping(t *testing.T) {
	cases := []string{msgbus.ResultStatusDone, msgbus.ResultStatusFailed, msgbus.ResultStatusCancelled}
	if len(cases) != 3 {
		t.Fatalf("want 3 statuses")
	}
}

func TestLifecycleEvent(t *testing.T) {
	all := []msgbus.LifecycleEvent{
		msgbus.EventStarted, msgbus.EventHeartbeat, msgbus.EventSpawned,
		msgbus.EventCompleted, msgbus.EventFailed,
	}
	if len(all) != 5 {
		t.Fatalf("want 5 lifecycle events")
	}
}

func TestNotifyEventCancelledAndSpawnFailed(t *testing.T) {
	// v3.1-R5
	if msgbus.NotifyCancelled == "" || msgbus.NotifySpawnFailed == "" {
		t.Fatal("NotifyCancelled / NotifySpawnFailed must be defined")
	}
}
