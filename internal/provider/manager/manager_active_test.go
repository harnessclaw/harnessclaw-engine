package manager

import (
	"testing"

	"harnessclaw-go/internal/config"
)

func TestActiveModelKey_PrimaryWins(t *testing.T) {
	m := &Manager{agent: config.AgentConfig{Primary: "anthropic:claude-opus-4-7"}}
	if got := m.ActiveModelKey(); got != "anthropic:claude-opus-4-7" {
		t.Errorf("got %q", got)
	}
}

func TestActiveModelKey_EmptyWhenNoPrimary(t *testing.T) {
	m := &Manager{}
	if got := m.ActiveModelKey(); got != "" {
		t.Errorf("got %q", got)
	}
}
