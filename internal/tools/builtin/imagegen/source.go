package imagegen

import (
	"strings"
	"sync"

	"harnessclaw-go/internal/config"
)

// ImageGenSource is the read seam the image tool uses. AgentImageGeneration
// returns the live selector; ResolveEndpoint parses + validates a
// "provider:endpoint" ref and fills credentials + path. ok=false means
// unparseable, unknown provider/endpoint, or empty api_key.
type ImageGenSource interface {
	AgentImageGeneration() string
	ResolveEndpoint(ref string) (ImageEndpointRef, bool)
}

// Source is the concrete ImageGenSource. Owns a live copy of cfg.ImageGen
// (mutated at runtime by the imagegenmgmt handler via UpdateProviders) and
// reads the agent selector from the manager.
type Source struct {
	mu       sync.RWMutex
	imageCfg config.ImageGenConfig
	agent    AgentConfigSource
}

func NewSource(imageCfg config.ImageGenConfig, agent AgentConfigSource) *Source {
	return &Source{imageCfg: imageCfg, agent: agent}
}

func (s *Source) AgentImageGeneration() string {
	if s.agent == nil {
		return ""
	}
	return strings.TrimSpace(s.agent.CurrentAgent().ImageGeneration)
}

func (s *Source) ResolveEndpoint(ref string) (ImageEndpointRef, bool) {
	prov, ep, err := config.ParseChainEntry(ref)
	if err != nil {
		return ImageEndpointRef{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.imageCfg.Providers[prov]
	if !ok {
		return ImageEndpointRef{}, false
	}
	epCfg, ok := p.Endpoints[ep]
	if !ok {
		return ImageEndpointRef{}, false
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return ImageEndpointRef{}, false
	}
	return ImageEndpointRef{
		Provider: prov,
		Endpoint: ep,
		Model:    epCfg.Model,
		APIKey:   p.APIKey,
		BaseURL:  strings.TrimSpace(p.BaseURL),
		Path:     strings.TrimSpace(p.Path),
		// AuthHeader/AuthPrefix left empty → provider defaults to
		// "Authorization" / "Bearer ".
	}, true
}

// UpdateProviders replaces the live image config (called by imagegenmgmt after
// a successful PATCH so the running tool immediately sees new credentials).
func (s *Source) UpdateProviders(cfg config.ImageGenConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imageCfg = cfg
}

// Snapshot returns a deep copy of the live image config for GET / persist.
func (s *Source) Snapshot() config.ImageGenConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneImageGenConfig(s.imageCfg)
}

func cloneImageGenConfig(in config.ImageGenConfig) config.ImageGenConfig {
	out := config.ImageGenConfig{Providers: make(map[string]config.ImageProviderConfig, len(in.Providers))}
	for name, p := range in.Providers {
		eps := make(map[string]config.ImageEndpointConfig, len(p.Endpoints))
		for k, v := range p.Endpoints {
			eps[k] = v
		}
		out.Providers[name] = config.ImageProviderConfig{
			APIKey:    p.APIKey,
			BaseURL:   p.BaseURL,
			Path:      p.Path,
			Endpoints: eps,
		}
	}
	return out
}
