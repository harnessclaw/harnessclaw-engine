package videogen

import (
	"fmt"
	"sort"
	"sync"
)

// ProviderRegistry maps provider name → implementation. Populated once at
// startup in main.go; reads are concurrent-safe.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]VideoProvider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]VideoProvider)}
}

func (r *ProviderRegistry) Register(p VideoProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := p.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("videogen: provider already registered: %s", name)
	}
	r.providers[name] = p
	return nil
}

func (r *ProviderRegistry) Get(name string) (VideoProvider, bool) {
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
