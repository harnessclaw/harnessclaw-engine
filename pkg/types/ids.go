// Package types — typed string IDs shared across engine layers.
package types

// AgentID 唯一标识一个 agent 实例（一次 spawn 的产物）。
// 约定前缀 "a-"。
type AgentID string

// TaskID 唯一标识 scheduler 跟踪的 task。
// 约定前缀 "t-"。L2 时代是 sequential ("t-0","t-1")，新 scheduler 用 UUID 后缀。
type TaskID string

// SessionID 标识一段会话上下文（session.Manager 维护）。
type SessionID string
