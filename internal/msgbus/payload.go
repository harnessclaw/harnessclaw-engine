package msgbus

// ----------------- KindTask payload -----------------

// TaskMessage is the L2→L3 dispatch payload. Body kept minimal—consumer reads
// full LeafSpec from tstate.Reader using TaskID.
type TaskMessage struct {
	TaskID   string `json:"task_id"`
	TaskType string `json:"task_type"` // phase 1: always "leaf"
	Task     string `json:"task"`       // natural-language goal
}

// ----------------- KindResult payload -----------------

// ResultMessage is the L3→L2 result payload.
type ResultMessage struct {
	TaskID     string `json:"task_id"`
	TaskType   string `json:"task_type"`
	OutputFile string `json:"output_file"`         // path relative to sessionRoot (not forced to meta.json)
	Status     string `json:"status"`              // ResultStatusDone | Failed | Cancelled
	Summary    string `json:"summary"`             // ≤ 200 chars
	Reason     string `json:"reason,omitempty"`    // "<code>: <free text>" format
}

// ResultMessage.Status mapping (v3.1-R9):
//   ResultStatusDone      → tstate.StatusSucceeded
//   ResultStatusFailed    → tstate.StatusFailed (or retry per FailureReason)
//   ResultStatusCancelled → tstate.StatusCancelled
const (
	ResultStatusDone      = "done"
	ResultStatusFailed    = "failed"
	ResultStatusCancelled = "cancelled"
)

// ----------------- KindLifecycle payload -----------------

// LifecyclePayload is sent only by L3 sub-agent workers; reaper/scheduler MUST NOT.
// From field must match "agent:<task_id>" pattern (validated in onLifecycle).
type LifecyclePayload struct {
	Event         LifecycleEvent `json:"event"`
	Attempt       int            `json:"attempt"`                 // epoch anti-stale
	ResultRef     string         `json:"result_ref,omitempty"`    // path on completed
	FailureReason string         `json:"failure_reason,omitempty"`
	ErrMsg        string         `json:"err_msg,omitempty"`        // truncated to 4KB
	SpawnedIDs    []string       `json:"spawned_ids,omitempty"`
}

type LifecycleEvent string

const (
	EventStarted   LifecycleEvent = "started"
	EventHeartbeat LifecycleEvent = "heartbeat"
	EventSpawned   LifecycleEvent = "spawned"
	EventCompleted LifecycleEvent = "completed"
	EventFailed    LifecycleEvent = "failed"
)

// ----------------- KindControl payload -----------------

type ControlPayload struct {
	Cmd  ControlCmd `json:"cmd"`
	Body any        `json:"body,omitempty"`
}

type ControlCmd string

const (
	CmdCancel ControlCmd = "cancel"
	CmdPause  ControlCmd = "pause"
	CmdSpawn  ControlCmd = "spawn"
)

// SpawnBody is the Body for ControlCmd=CmdSpawn. Uses []any to avoid msgbus→spec import.
type SpawnBody struct {
	Specs []any `json:"specs"` // each is a spec.TaskSpec
}

// ----------------- KindAgentMsg payload -----------------

type AgentMsgPayload struct {
	Text       string `json:"text,omitempty"`
	ToolResult any    `json:"tool_result,omitempty"`
}

// ----------------- KindNotify payload -----------------

type NotifyPayload struct {
	Event      NotifyEvent `json:"event"`
	TaskID     string      `json:"task_id"`
	SpawnedIDs []string    `json:"spawned_ids,omitempty"`
	Reason     string      `json:"reason,omitempty"`
}

type NotifyEvent string

const (
	NotifyReady                NotifyEvent = "ready"
	NotifyWoken                NotifyEvent = "woken"
	NotifySucceeded            NotifyEvent = "succeeded"
	NotifyFailed               NotifyEvent = "failed"
	NotifyCancelled            NotifyEvent = "cancelled"               // v3.1-R5
	NotifyLeaseExpired         NotifyEvent = "lease_expired"
	NotifyDeadlineExceeded     NotifyEvent = "deadline_exceeded"
	NotifyCancellingDrained    NotifyEvent = "cancelling_drained"
	NotifyCompletedFromStaging NotifyEvent = "completed_from_staging"
	NotifySpawnGranted         NotifyEvent = "spawn_granted"
	NotifySpawnFailed          NotifyEvent = "spawn_failed"            // v3.1-R4
)
