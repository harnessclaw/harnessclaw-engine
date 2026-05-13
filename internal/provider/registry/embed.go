package registry

import (
	_ "embed"
	"fmt"
)

//go:embed default-manifest.yaml
var defaultManifestYAML []byte

// DefaultManifest returns the manifest bundled with the binary. The
// engine loads this at startup; operators can override individual
// entries by supplying a custom path via configuration.
func DefaultManifest() (*Manifest, error) {
	m, err := LoadManifestFromYAML(defaultManifestYAML)
	if err != nil {
		return nil, fmt.Errorf("parse embedded default-manifest.yaml: %w", err)
	}
	return m, nil
}
