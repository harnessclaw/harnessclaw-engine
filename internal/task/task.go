package task

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
)

// Task represents a unit of work in the task system.
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Status      TaskStatus     `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	ActiveForm  string         `json:"active_form,omitempty"`
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	ScopeID     string         `json:"scope_id"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// TaskUpdate carries optional fields for updating a task.
type TaskUpdate struct {
	Subject      *string        `json:"subject,omitempty"`
	Description  *string        `json:"description,omitempty"`
	Status       *TaskStatus    `json:"status,omitempty"`
	Owner        *string        `json:"owner,omitempty"`
	ActiveForm   *string        `json:"active_form,omitempty"`
	AddBlocks    []string       `json:"add_blocks,omitempty"`
	AddBlockedBy []string       `json:"add_blocked_by,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"` // nil key = delete
}
