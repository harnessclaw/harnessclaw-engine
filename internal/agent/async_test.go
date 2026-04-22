package agent

import (
	"testing"
	"time"
)

func TestAgentRegistry_RegisterAndGet(t *testing.T) {
	reg := NewAgentRegistry()
	a := &AsyncAgent{
		ID:        "async_test1",
		Name:      "worker-1",
		Status:    AgentStatusRunning,
		StartedAt: time.Now(),
	}
	reg.Register(a)

	got := reg.Get("async_test1")
	if got == nil {
		t.Fatal("expected to find agent")
	}
	if got.Name != "worker-1" {
		t.Errorf("expected name worker-1, got %s", got.Name)
	}
}

func TestAgentRegistry_GetByName(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Name: "researcher", Status: AgentStatusRunning, StartedAt: time.Now()})
	reg.Register(&AsyncAgent{ID: "a2", Name: "coder", Status: AgentStatusRunning, StartedAt: time.Now()})

	got := reg.GetByName("coder")
	if got == nil || got.ID != "a2" {
		t.Errorf("expected agent a2, got %v", got)
	}

	got = reg.GetByName("nonexistent")
	if got != nil {
		t.Errorf("expected nil for unknown name, got %v", got)
	}
}

func TestAgentRegistry_SetStatus(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Status: AgentStatusRunning, StartedAt: time.Now()})

	reg.SetStatus("a1", AgentStatusCompleted)
	a := reg.Get("a1")
	if a.Status != AgentStatusCompleted {
		t.Errorf("expected completed, got %s", a.Status)
	}
	if a.EndedAt.IsZero() {
		t.Error("expected EndedAt to be set")
	}
}

func TestAgentRegistry_SetResult(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Status: AgentStatusRunning, StartedAt: time.Now()})

	result := &SpawnResult{Output: "done", NumTurns: 3}
	reg.SetResult("a1", result, nil)

	a := reg.Get("a1")
	if a.Result == nil || a.Result.Output != "done" {
		t.Errorf("expected result with output 'done', got %v", a.Result)
	}
}

func TestAgentRegistry_List(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Status: AgentStatusRunning, StartedAt: time.Now()})
	reg.Register(&AsyncAgent{ID: "a2", Status: AgentStatusCompleted, StartedAt: time.Now()})
	reg.Register(&AsyncAgent{ID: "a3", Status: AgentStatusRunning, StartedAt: time.Now()})

	running := reg.List(AgentStatusRunning)
	if len(running) != 2 {
		t.Errorf("expected 2 running agents, got %d", len(running))
	}

	all := reg.List("")
	if len(all) != 3 {
		t.Errorf("expected 3 total agents, got %d", len(all))
	}
}

func TestAgentRegistry_Cancel(t *testing.T) {
	reg := NewAgentRegistry()
	cancelled := false
	reg.Register(&AsyncAgent{
		ID:        "a1",
		Status:    AgentStatusRunning,
		StartedAt: time.Now(),
		Cancel:    func() { cancelled = true },
	})

	err := reg.Cancel("a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cancelled {
		t.Error("expected cancel func to be called")
	}

	a := reg.Get("a1")
	if a.Status != AgentStatusCancelled {
		t.Errorf("expected cancelled, got %s", a.Status)
	}
}

func TestAgentRegistry_Cancel_NotRunning(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Status: AgentStatusCompleted, StartedAt: time.Now()})

	err := reg.Cancel("a1")
	if err == nil {
		t.Error("expected error cancelling non-running agent")
	}
}

func TestAgentRegistry_Cancel_NotFound(t *testing.T) {
	reg := NewAgentRegistry()
	err := reg.Cancel("nonexistent")
	if err == nil {
		t.Error("expected error for unknown agent")
	}
}

func TestAgentRegistry_Remove(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&AsyncAgent{ID: "a1", Status: AgentStatusCompleted, StartedAt: time.Now()})
	reg.Remove("a1")

	if reg.Get("a1") != nil {
		t.Error("expected agent to be removed")
	}
}

func TestNewAsyncAgentID(t *testing.T) {
	id := NewAsyncAgentID()
	if len(id) == 0 {
		t.Error("expected non-empty ID")
	}
	if id[:6] != "async_" {
		t.Errorf("expected prefix 'async_', got %q", id)
	}
}
