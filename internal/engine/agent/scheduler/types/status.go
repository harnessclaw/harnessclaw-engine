package types

// Status is the 8-state task state machine.
//   pending → ready → running → succeeded
//                            → failed → ready (retry)
//                            → waiting → running
//   * non-terminal → cancelling → cancelled
type Status string

const (
	StatusPending    Status = "pending"
	StatusReady      Status = "ready"
	StatusRunning    Status = "running"
	StatusWaiting    Status = "waiting"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelling Status = "cancelling"
	StatusCancelled  Status = "cancelled"
)

// IsTerminal reports whether s is a terminal state (no further transitions).
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	}
	return false
}
