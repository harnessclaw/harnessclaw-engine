package types

import "time"

// Budget caps the resources a task may consume before being killed.
// MaxCost uses integer cents to avoid floats.
type Budget struct {
	MaxTokens   int64     `json:"max_tokens"`
	MaxCost     int64     `json:"max_cost"`
	Deadline    time.Time `json:"deadline"`
	MaxFailures int       `json:"max_failures"`
}

// ResourceReq describes upfront resource expectations for a task.
type ResourceReq struct {
	Model       string        `json:"model"`
	EstTokens   int64         `json:"est_tokens"`
	BackendHint string        `json:"backend_hint"` // "in-process" only in phase 1
	LeaseTTL    time.Duration `json:"lease_ttl"`    // 0 → scheduler default
}

// Lease records the currently-claimed worker and expiry.
type Lease struct {
	WorkerID  string    `json:"worker_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
