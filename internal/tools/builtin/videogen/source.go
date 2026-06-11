package videogen

import (
	"strings"
	"sync"

	"harnessclaw-go/internal/config"
)

// ConfigSource is the read seam the tools use. AgentVideoGeneration returns the
// live selector; ResolveEndpoint parses + validates a "provider:endpoint" ref
// and fills credentials. ok=false means: unparseable, unknown provider/endpoint,
// or empty api_key.
type ConfigSource interface {
	AgentVideoGeneration() string
	ResolveEndpoint(ref string) (EndpointRef, bool)
}

// AgentConfigSource is satisfied by the provider manager (CurrentAgent). It lets
// the Source read the live agent.video_generation selector without owning the
// agent config (the manager owns it so it round-trips with the rest of agent.*).
type AgentConfigSource interface {
	CurrentAgent() config.AgentConfig
}

// Source is the concrete ConfigSource. It owns a live copy of cfg.VideoGen
// (mutated at runtime by the videogenmgmt handler via UpdateProviders) and reads
// the agent selector from the manager.
type Source struct {
	mu       sync.RWMutex
	videoCfg config.VideoGenConfig
	agent    AgentConfigSource
}

func NewSource(videoCfg config.VideoGenConfig, agent AgentConfigSource) *Source {
	return &Source{videoCfg: videoCfg, agent: agent}
}

func (s *Source) AgentVideoGeneration() string {
	if s.agent == nil {
		return ""
	}
	return strings.TrimSpace(s.agent.CurrentAgent().VideoGeneration)
}

func (s *Source) ResolveEndpoint(ref string) (EndpointRef, bool) {
	prov, ep, err := config.ParseChainEntry(ref)
	if err != nil {
		return EndpointRef{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.videoCfg.Providers[prov]
	if !ok {
		return EndpointRef{}, false
	}
	epCfg, ok := p.Endpoints[ep]
	if !ok {
		return EndpointRef{}, false
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return EndpointRef{}, false
	}
	return EndpointRef{
		Provider: prov,
		Endpoint: ep,
		Model:    epCfg.Model,
		APIKey:   p.APIKey,
		BaseURL:  strings.TrimSpace(p.BaseURL),
	}, true
}

// UpdateProviders replaces the live video config (called by videogenmgmt after a
// successful PATCH so running tools immediately see new credentials).
func (s *Source) UpdateProviders(cfg config.VideoGenConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videoCfg = cfg
}

// Snapshot returns a copy of the live video config for GET responses / persist.
func (s *Source) Snapshot() config.VideoGenConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneVideoGenConfig(s.videoCfg)
}

func cloneVideoGenConfig(in config.VideoGenConfig) config.VideoGenConfig {
	out := config.VideoGenConfig{Providers: make(map[string]config.VideoProviderConfig, len(in.Providers))}
	for name, p := range in.Providers {
		eps := make(map[string]config.VideoEndpointConfig, len(p.Endpoints))
		for k, v := range p.Endpoints {
			eps[k] = v
		}
		out.Providers[name] = config.VideoProviderConfig{
			APIKey:    p.APIKey,
			BaseURL:   p.BaseURL,
			Endpoints: eps,
		}
	}
	return out
}
