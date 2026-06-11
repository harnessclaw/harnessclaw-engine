package session

import (
	"testing"
)

func TestSession_AllowedToolLifecycle(t *testing.T) {
	s := &Session{}
	if s.IsToolAllowed("Read") {
		t.Error("fresh session reports Read as allowed; want false")
	}
	s.RememberAllowedTool("Read")
	if !s.IsToolAllowed("Read") {
		t.Error("after RememberAllowedTool, IsToolAllowed returns false")
	}
}

func TestSession_AllowedTools_List(t *testing.T) {
	s := &Session{}
	if got := s.AllowedTools(); got != nil {
		t.Errorf("empty session AllowedTools should return nil, got %v", got)
	}
	s.RememberAllowedTool("Read")
	s.RememberAllowedTool("Bash")
	got := s.AllowedTools()
	if len(got) != 2 {
		t.Errorf("expected 2 allowed tools, got %d (%v)", len(got), got)
	}
}
