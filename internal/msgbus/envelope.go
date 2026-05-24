// Package msgbus is the L2↔L3 communication bus, persisted via internal/storage/sqlite.
package msgbus

import "time"

// Address is the message sender/receiver identifier (stub; full type in address.go from Task 9)
type Address string

// AgentMessage is the unified envelope for all 6 Kind types.
type AgentMessage struct {
	MsgID       string    `json:"msg_id"`
	Kind        Kind      `json:"kind"`
	From        Address   `json:"from"`
	To          Address   `json:"to"`
	TaskID      string    `json:"task_id"`
	SessionID   string    `json:"session_id"`
	CausationID string    `json:"causation_id,omitempty"`
	Ts          time.Time `json:"ts"`
	Payload     any       `json:"payload"`
}
