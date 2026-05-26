package msgbus

import "strings"

// Address routes messages within the bus.
//   "agent:<task_id>"     → specific task mailbox (L2 host or L3 runner)
//   "queue:<name>"        → consumer pool queue (e.g. "queue:leaf")
//   "scheduler"           → L2 top-level handlers
//   "broadcast:<sid>"     → session-wide broadcast
//   "reaper"              → sender identifier only (not subscribable)
type Address string

const (
	AddrScheduler Address = "scheduler"
	AddrReaper    Address = "reaper"
)

// AddrAgent returns an address pointing to a task's mailbox.
func AddrAgent(taskID string) Address { return Address("agent:" + taskID) }

// AddrQueue returns an address pointing to a named consumer pool queue.
func AddrQueue(name string) Address { return Address("queue:" + name) }

// AddrBroadcast returns an address for a session-wide broadcast.
func AddrBroadcast(sessionID string) Address { return Address("broadcast:" + sessionID) }

// AgentTaskID extracts the task ID from an "agent:<tid>" address.
func (a Address) AgentTaskID() (string, bool) {
	s := string(a)
	if !strings.HasPrefix(s, "agent:") {
		return "", false
	}
	return strings.TrimPrefix(s, "agent:"), true
}

// QueueName extracts the queue name from a "queue:<name>" address.
func (a Address) QueueName() (string, bool) {
	s := string(a)
	if !strings.HasPrefix(s, "queue:") {
		return "", false
	}
	return strings.TrimPrefix(s, "queue:"), true
}
