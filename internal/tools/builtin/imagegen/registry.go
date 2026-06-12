package imagegen

import (
	"fmt"
	"sort"
	"sync"
)

// ProviderRegistry maps provider name → ImageProvider implementation.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]ImageProvider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]ImageProvider)}
}

func (r *ProviderRegistry) Register(p ImageProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := p.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("imagegen: provider already registered: %s", name)
	}
	r.providers[name] = p
	return nil
}

func (r *ProviderRegistry) Get(name string) (ImageProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

func (r *ProviderRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
