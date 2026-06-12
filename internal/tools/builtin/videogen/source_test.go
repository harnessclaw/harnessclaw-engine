package videogen

import (
	"testing"

	"harnessclaw-go/internal/config"
)

type fakeAgentSource struct{ ref string }

func (f fakeAgentSource) CurrentAgent() config.AgentConfig {
	return config.AgentConfig{VideoGeneration: f.ref}
}

func newTestVideoCfg(apiKey, baseURL string) config.VideoGenConfig {
	return config.VideoGenConfig{
		Providers: map[string]config.VideoProviderConfig{
			"doubao": {
				APIKey:  apiKey,
				BaseURL: baseURL,
				Endpoints: map[string]config.VideoEndpointConfig{
					"seedance-lite-i2v": {Model: "doubao-seedance-1-0-lite-i2v-250428"},
				},
			},
		},
	}
}

func TestSourceResolveEndpoint(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestVideoCfg("sk-x", "https://base"), fakeAgentSource{ref: "doubao:seedance-lite-i2v"})

	if got := s.AgentVideoGeneration(); got != "doubao:seedance-lite-i2v" {
		t.Fatalf("AgentVideoGeneration = %q", got)
	}
	ep, ok := s.ResolveEndpoint("doubao:seedance-lite-i2v")
	if !ok {
		t.Fatal("valid ref should resolve")
	}
	if ep.Provider != "doubao" || ep.Endpoint != "seedance-lite-i2v" ||
		ep.Model != "doubao-seedance-1-0-lite-i2v-250428" ||
		ep.APIKey != "sk-x" || ep.BaseURL != "https://base" {
		t.Fatalf("resolved endpoint mismatch: %+v", ep)
	}
	if _, ok := s.ResolveEndpoint("doubao:missing"); ok {
		t.Fatal("unknown endpoint must not resolve")
	}
	if _, ok := s.ResolveEndpoint("garbage"); ok {
		t.Fatal("bad ref must not resolve")
	}
}

func TestSourceEmptyAPIKeyNotResolvable(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestVideoCfg("", ""), fakeAgentSource{ref: "doubao:seedance-lite-i2v"})
	if _, ok := s.ResolveEndpoint("doubao:seedance-lite-i2v"); ok {
		t.Fatal("empty api_key endpoint must not resolve")
	}
}

func TestSourceUpdateAndSnapshot(t *testing.T) {
	t.Parallel()
	s := NewSource(newTestVideoCfg("", ""), fakeAgentSource{ref: "doubao:seedance-lite-i2v"})
	if _, ok := s.ResolveEndpoint("doubao:seedance-lite-i2v"); ok {
		t.Fatal("precondition: empty key not resolvable")
	}
	s.UpdateProviders(newTestVideoCfg("sk-live", "https://base2"))
	ep, ok := s.ResolveEndpoint("doubao:seedance-lite-i2v")
	if !ok || ep.APIKey != "sk-live" || ep.BaseURL != "https://base2" {
		t.Fatalf("update not reflected: %+v ok=%v", ep, ok)
	}
	snap := s.Snapshot()
	if snap.Providers["doubao"].APIKey != "sk-live" {
		t.Fatal("snapshot should reflect update")
	}
}
