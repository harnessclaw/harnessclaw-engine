// Package persist reads/writes the engine's yaml config in a way
// that preserves comments and key order. It is used by the
// management API to write provider / fallback_chain edits back to
// the source file without scrubbing the user's annotations.
//
// The implementation operates on yaml.Node trees rather than
// re-marshalling a typed struct. This is verbose but is the only
// approach that survives a round-trip through user-authored config:
// re-marshalling a parsed config.Config drops every comment and
// re-orders fields alphabetically, which is unacceptable for
// hand-edited files.
package persist

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"harnessclaw-go/internal/config"
)

// File is a parsed yaml config file with its AST plus the on-disk
// path. All mutators run against the AST; Save serialises it back to
// disk atomically (write to <path>.tmp, then rename).
type File struct {
	path string
	root yaml.Node
}

// Load reads and parses the yaml file at path.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("persist: read %s: %w", path, err)
	}
	f := &File{path: path}
	if err := yaml.Unmarshal(data, &f.root); err != nil {
		return nil, fmt.Errorf("persist: parse %s: %w", path, err)
	}
	if f.root.Kind != yaml.DocumentNode || len(f.root.Content) == 0 {
		return nil, fmt.Errorf("persist: %s: empty or non-document yaml", path)
	}
	if f.root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("persist: %s: root is not a mapping", path)
	}
	return f, nil
}

// Save atomically writes the (possibly modified) AST back to the
// original path. Uses tmp + rename so a crash mid-write leaves the
// previous file intact.
func (f *File) Save() error {
	data, err := yaml.Marshal(&f.root)
	if err != nil {
		return fmt.Errorf("persist: marshal: %w", err)
	}
	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, ".cfg-*")
	if err != nil {
		return fmt.Errorf("persist: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // safe even if rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("persist: write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("persist: close tmp: %w", err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("persist: rename: %w", err)
	}
	return nil
}

// SetFallbackChain replaces llm.fallback_chain with the given chain.
// Creates the field if it doesn't exist. Empty chain removes the
// field entirely.
func (f *File) SetFallbackChain(chain []string) error {
	llm, err := f.llmNode()
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		removeKey(llm, "fallback_chain")
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, name := range chain {
		seq.Content = append(seq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: name,
		})
	}
	setKey(llm, "fallback_chain", seq)
	return nil
}

// SetProvider replaces or inserts llm.providers[name] with the given
// ProviderConfig. Only the fields actually present in cfg are
// emitted (zero-valued fields are skipped).
func (f *File) SetProvider(name string, cfg config.ProviderConfig) error {
	llm, err := f.llmNode()
	if err != nil {
		return err
	}
	providers, idx := findValue(llm, "providers")
	if providers == nil {
		providers = &yaml.Node{Kind: yaml.MappingNode}
		setKey(llm, "providers", providers)
		_ = idx
	}
	if providers.Kind != yaml.MappingNode {
		return fmt.Errorf("persist: llm.providers is not a mapping")
	}
	provNode := providerToNode(cfg)
	setKey(providers, name, provNode)
	return nil
}

// llmNode returns the `llm` mapping under the document root.
func (f *File) llmNode() (*yaml.Node, error) {
	root := f.root.Content[0]
	llm, _ := findValue(root, "llm")
	if llm == nil {
		llm = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "llm", llm)
	}
	if llm.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("persist: top-level llm is not a mapping")
	}
	return llm, nil
}

// providerToNode builds a Mapping node from a ProviderConfig. Empty
// fields are omitted so a round-trip doesn't introduce zero-valued
// keys that weren't in the source.
func providerToNode(p config.ProviderConfig) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	if p.BaseURL != "" {
		appendScalar(n, "base_url", p.BaseURL)
	}
	if p.APIKey != "" {
		appendScalar(n, "api_key", p.APIKey)
	}
	if p.Model != "" {
		appendScalar(n, "model", p.Model)
	}
	if p.MaxTokens != 0 {
		appendInt(n, "max_tokens", p.MaxTokens)
	}
	if p.Temperature != 0 {
		appendFloat(n, "temperature", p.Temperature)
	}
	if p.EnableThinking != nil {
		appendBool(n, "enable_thinking", *p.EnableThinking)
	}
	return n
}

// findValue locates the value paired with key in a Mapping node.
// Returns (nil, -1) when absent.
func findValue(m *yaml.Node, key string) (*yaml.Node, int) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], i + 1
		}
	}
	return nil, -1
}

// setKey inserts or replaces key→value in a Mapping. New keys are
// appended at the end (preserving existing comment ordering).
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Preserve the existing key node's comments by mutating
			// the value in place rather than replacing the key.
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

// removeKey deletes a key from a Mapping (no-op if absent).
func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// appendScalar adds key: "value" to a Mapping node.
func appendScalar(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

// appendInt adds key: <int> to a Mapping node.
func appendInt(m *yaml.Node, key string, value int) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)},
	)
}

// appendFloat adds key: <float> to a Mapping node.
func appendFloat(m *yaml.Node, key string, value float64) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: fmt.Sprintf("%g", value)},
	)
}

// appendBool adds key: <bool> to a Mapping node.
func appendBool(m *yaml.Node, key string, value bool) {
	v := "false"
	if value {
		v = "true"
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v},
	)
}
