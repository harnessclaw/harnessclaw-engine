package msgbus

// Kind enumerates the 6 envelope types.
type Kind string

const (
	KindLifecycle Kind = "task.lifecycle"
	KindControl   Kind = "control"
	KindAgentMsg  Kind = "agent.message"
	KindNotify    Kind = "schedule.notify"
	KindTask      Kind = "task"
	KindResult    Kind = "result"
)
