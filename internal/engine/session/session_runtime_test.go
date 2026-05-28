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

func TestSession_PromptCacheRoundtrip(t *testing.T) {
	s := &Session{}
	if s.PromptCache() != nil {
		t.Error("fresh session PromptCache is non-nil")
	}
	entry := &PromptCacheEntry{Prompt: "hello"}
	s.SetPromptCache(entry)
	got := s.PromptCache()
	if got == nil || got.Prompt != "hello" {
		t.Errorf("PromptCache roundtrip failed; got %#v", got)
	}
	s.SetPromptCache(nil)
	if s.PromptCache() != nil {
		t.Error("SetPromptCache(nil) did not clear the cache")
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
