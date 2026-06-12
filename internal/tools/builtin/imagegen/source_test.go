package imagegen

import (
	"testing"

	"harnessclaw-go/internal/config"
)

type fakeAgentSource struct{ ref string }

func (f fakeAgentSource) CurrentAgent() config.AgentConfig {
	return config.AgentConfig{ImageGeneration: f.ref}
}

func newTestImageCfg(apiKey, baseURL, path string) config.ImageGenConfig {
	return config.ImageGenConfig{
		Providers: map[string]config.ImageProviderConfig{
			"openai": {
				APIKey:  apiKey,
				BaseURL: baseURL,
				Path:    path,
				Endpoints: map[string]config.ImageEndpointConfig{
					"gpt-image": {Model: "gpt-image-1"},
				},
			},
		},
	}
}

func TestImageSourceResolve(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestImageCfg("sk-x", "https://api.openai.com", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	if got := s.AgentImageGeneration(); got != "openai:gpt-image" {
		t.Fatalf("AgentImageGeneration = %q", got)
	}
	ep, ok := s.ResolveEndpoint("openai:gpt-image")
	if !ok {
		t.Fatal("valid ref should resolve")
	}
	if ep.Provider != "openai" || ep.Endpoint != "gpt-image" || ep.Model != "gpt-image-1" ||
		ep.APIKey != "sk-x" || ep.BaseURL != "https://api.openai.com" || ep.Path != "/v1/images/generations" {
		t.Fatalf("resolved mismatch: %+v", ep)
	}
	if _, ok := s.ResolveEndpoint("openai:missing"); ok {
		t.Fatal("unknown endpoint must not resolve")
	}
	if _, ok := s.ResolveEndpoint("garbage"); ok {
		t.Fatal("bad ref must not resolve")
	}
}

func TestImageSourceEmptyKey(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestImageCfg("", "", ""), fakeAgentSource{ref: "openai:gpt-image"})
	if _, ok := s.ResolveEndpoint("openai:gpt-image"); ok {
		t.Fatal("empty api_key must not resolve")
	}
}

func TestImageSourceUpdateSnapshot(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestImageCfg("", "", ""), fakeAgentSource{ref: "openai:gpt-image"})
	if _, ok := s.ResolveEndpoint("openai:gpt-image"); ok {
		t.Fatal("precondition empty key")
	}
	s.UpdateProviders(newTestImageCfg("sk-live", "https://b", "/p"))
	ep, ok := s.ResolveEndpoint("openai:gpt-image")
	if !ok || ep.APIKey != "sk-live" || ep.Path != "/p" {
		t.Fatalf("update not reflected: %+v ok=%v", ep, ok)
	}
	if s.Snapshot().Providers["openai"].APIKey != "sk-live" {
		t.Fatal("snapshot should reflect update")
	}
}
