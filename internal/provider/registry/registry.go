package registry

import "sort"

// Registry is a read-only view over a Manifest. Once constructed it is
// safe for concurrent reads without locking. Replacement (e.g. for
// future hot-reload) happens by swapping the pointer atomically in the
// consumer's reference.
type Registry struct {
	manifest *Manifest

	// modelByProviderAndID lets the bifrost adapter look up a model
	// without knowing the manifest key. Built once at construction.
	modelByProviderAndID map[string]map[string]*ModelSpec
}

// NewRegistry wraps a Manifest into a Registry, building secondary
// indexes for the common runtime lookups.
func NewRegistry(m *Manifest) *Registry {
	r := &Registry{
		manifest:             m,
		modelByProviderAndID: make(map[string]map[string]*ModelSpec),
	}
	for _, mod := range m.Models {
		bucket := r.modelByProviderAndID[mod.Provider]
		if bucket == nil {
			bucket = make(map[string]*ModelSpec)
			r.modelByProviderAndID[mod.Provider] = bucket
		}
		bucket[mod.ModelID] = mod
	}
	return r
}

// LookupModel returns the model spec for a manifest key
// ("provider/model_id") or nil if absent.
func (r *Registry) LookupModel(key string) *ModelSpec {
	if r == nil {
		return nil
	}
	return r.manifest.Models[key]
}

// LookupProvider returns the provider spec for a provider name or nil.
func (r *Registry) LookupProvider(name string) *ProviderSpec {
	if r == nil {
		return nil
	}
	return r.manifest.Providers[name]
}

// LookupByProviderAndModelID finds a model by its provider name and the
// wire-level model_id field.
func (r *Registry) LookupByProviderAndModelID(provider, modelID string) *ModelSpec {
	if r == nil {
		return nil
	}
	bucket := r.modelByProviderAndID[provider]
	if bucket == nil {
		return nil
	}
	return bucket[modelID]
}

// ListModels returns the manifest keys of every known model, sorted
// lexicographically for stable UI ordering.
func (r *Registry) ListModels() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.manifest.Models))
	for k := range r.manifest.Models {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Manifest exposes the underlying manifest for callers (HTTP handler)
// that need to serialize the whole catalog. The returned pointer must
// not be mutated.
func (r *Registry) Manifest() *Manifest {
	if r == nil {
		return nil
	}
	return r.manifest
}
