package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AgentStatus represents the lifecycle state of an async agent.
type AgentStatus string

const (
	AgentStatusRunning   AgentStatus = "running"
	AgentStatusIdle      AgentStatus = "idle"
	AgentStatusCompleted AgentStatus = "completed"
	AgentStatusFailed    AgentStatus = "failed"
	AgentStatusCancelled AgentStatus = "cancelled"
)

// AsyncAgent tracks the state of a background agent.
type AsyncAgent struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Status    AgentStatus `json:"status"`
	Config    *SpawnConfig `json:"-"`
	Result    *SpawnResult `json:"result,omitempty"`
	Error     error        `json:"-"`
	StartedAt time.Time   `json:"started_at"`
	EndedAt   time.Time   `json:"ended_at,omitempty"`
	Cancel    context.CancelFunc `json:"-"`
}

// AsyncSpawner extends AgentSpawner with async capabilities.
type AsyncSpawner interface {
	AgentSpawner
	SpawnAsync(ctx context.Context, cfg *SpawnConfig) (agentID string, err error)
}

// AgentRegistry tracks all running and completed agents.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*AsyncAgent // agentID -> agent
}

// NewAgentRegistry creates a new registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[string]*AsyncAgent),
	}
}

// Register adds an async agent to the registry.
func (r *AgentRegistry) Register(agent *AsyncAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.ID] = agent
}

// Get returns an agent by ID, or nil if not found.
func (r *AgentRegistry) Get(id string) *AsyncAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[id]
}

// GetByName returns the first agent matching the given name.
func (r *AgentRegistry) GetByName(name string) *AsyncAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// SetStatus updates an agent's status (thread-safe).
func (r *AgentRegistry) SetStatus(id string, status AgentStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.agents[id]; ok {
		a.Status = status
		if status == AgentStatusCompleted || status == AgentStatusFailed || status == AgentStatusCancelled {
			a.EndedAt = time.Now()
		}
	}
}

// SetResult updates an agent's result (thread-safe).
func (r *AgentRegistry) SetResult(id string, result *SpawnResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.agents[id]; ok {
		a.Result = result
		a.Error = err
	}
}

// List returns all agents matching the given status, or all if status is empty.
func (r *AgentRegistry) List(status AgentStatus) []*AsyncAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AsyncAgent
	for _, a := range r.agents {
		if status == "" || a.Status == status {
			result = append(result, a)
		}
	}
	return result
}

// Cancel cancels a running agent by ID.
func (r *AgentRegistry) Cancel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return fmt.Errorf("agent %q not found", id)
	}
	if a.Status != AgentStatusRunning {
		return fmt.Errorf("agent %q is not running (status: %s)", id, a.Status)
	}
	if a.Cancel != nil {
		a.Cancel()
	}
	a.Status = AgentStatusCancelled
	a.EndedAt = time.Now()
	return nil
}

// HasRunningForParent returns true if any async agent spawned by the given
// parent session is still running. Used by the query loop to decide whether
// to wait for mailbox notifications before terminating.
func (r *AgentRegistry) HasRunningForParent(parentSessionID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents {
		if a.Config != nil && a.Config.ParentSessionID == parentSessionID && a.Status == AgentStatusRunning {
			return true
		}
	}
	return false
}

// Remove removes a completed or cancelled agent from the registry.
func (r *AgentRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}

// NewAsyncAgentID generates a unique async agent ID.
func NewAsyncAgentID() string {
	return "async_" + uuid.New().String()[:8]
}
