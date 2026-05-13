package registry

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadManifestFromYAML parses a YAML byte slice into a Manifest and
// validates the cross-references between Providers and Models. Returns
// an error if any model references an unknown provider, or if a model's
// key prefix disagrees with its `provider` field (the key prefix is the
// canonical identifier — the field exists only for explicit readers).
func LoadManifestFromYAML(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// LoadManifestFromFile reads YAML from disk and parses it via
// LoadManifestFromYAML.
func LoadManifestFromFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return LoadManifestFromYAML(data)
}

func validateManifest(m *Manifest) error {
	if m.Providers == nil {
		m.Providers = make(map[string]*ProviderSpec)
	}
	if m.Models == nil {
		m.Models = make(map[string]*ModelSpec)
	}
	for key, mod := range m.Models {
		if mod == nil {
			return fmt.Errorf("model %q: nil entry", key)
		}
		if _, ok := m.Providers[mod.Provider]; !ok {
			return fmt.Errorf("model %q references unknown provider %q", key, mod.Provider)
		}
		slash := strings.IndexByte(key, '/')
		if slash <= 0 {
			return fmt.Errorf("model key %q must be 'provider/model_id'", key)
		}
		if key[:slash] != mod.Provider {
			return fmt.Errorf("model %q: key prefix %q != provider field %q",
				key, key[:slash], mod.Provider)
		}
	}
	return nil
}
