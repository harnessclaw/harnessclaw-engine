// Package persist reads/writes the engine's yaml config in a way
// that preserves comments and key order. The management API calls
// these helpers after applying mutations to the in-memory manager,
// so the on-disk yaml mirrors the live state.
//
// Implementation operates on yaml.Node trees rather than
// re-marshalling typed structs — the only approach that survives a
// round-trip through user-authored config (comments + key order +
// hand-tuned indentation).
package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"harnessclaw-go/internal/config"
)

// File is a parsed yaml config file with its AST plus the on-disk
// path. Mutators run against the AST; Save serialises it back to
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

// Save atomically writes the AST back to the original path
// (tmp + rename so a crash mid-write leaves the previous intact).
//
// Before marshalling, every mapping/sequence node has its Style
// reset to 0 (block). This neutralises yaml.v3's tendency to
// preserve flow style on nodes that were originally parsed from
// flow syntax (e.g. an empty `endpoints: {}` left by a prior
// marshal). Without this, a chain of edits could compact an
// entire branch into one unreadable flow-style line.
func (f *File) Save() error {
	forceBlockStyle(&f.root)
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
	defer os.Remove(tmpName)
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

// SetAgent rewrites the top-level `agent:` block with cfg, creating
// the block when absent. Empty cfg.Primary / cfg.FallbackChain remove
// the respective keys (degraded mode persists cleanly as either an
// absent key or an empty agent block).
//
// As a one-time migration, any stale `llm.fallback_chain` left over
// from the pre-agent config layout is removed on the same save —
// keeps the on-disk file from carrying both keys after the cutover.
func (f *File) SetAgent(cfg config.AgentConfig) error {
	// Drop stale llm.fallback_chain if present (one-time migration).
	if llm, _ := findValue(f.root.Content[0], "llm"); llm != nil && llm.Kind == yaml.MappingNode {
		removeKey(llm, "fallback_chain")
	}

	root := f.root.Content[0]
	agent, _ := findValue(root, "agent")
	if agent == nil {
		agent = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "agent", agent)
	}
	if agent.Kind != yaml.MappingNode {
		return fmt.Errorf("persist: top-level agent is not a mapping")
	}
	// Wipe all known fields, then re-emit in canonical order. Block
	// is small enough that comment loss on agent.* keys is acceptable
	// (user comments belong on the surrounding free-form yaml).
	for _, k := range []string{
		"primary", "fallback_chain", "image_generation",
		"max_tokens", "temperature", "context_window",
		"max_turns", "max_tool_calls", "thinking_intensity",
	} {
		removeKey(agent, k)
	}
	if cfg.Primary != "" {
		appendScalar(agent, "primary", cfg.Primary)
	}
	if len(cfg.FallbackChain) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, name := range cfg.FallbackChain {
			seq.Content = append(seq.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: name,
			})
		}
		setKey(agent, "fallback_chain", seq)
	}
	if cfg.ImageGeneration != "" {
		appendScalar(agent, "image_generation", cfg.ImageGeneration)
	}
	if cfg.MaxTokens != 0 {
		appendInt(agent, "max_tokens", cfg.MaxTokens)
	}
	if cfg.Temperature != 0 {
		appendFloat(agent, "temperature", cfg.Temperature)
	}
	if cfg.ContextWindow != 0 {
		appendInt(agent, "context_window", cfg.ContextWindow)
	}
	if cfg.MaxTurns != 0 {
		appendInt(agent, "max_turns", cfg.MaxTurns)
	}
	if cfg.MaxToolCalls != 0 {
		appendInt(agent, "max_tool_calls", cfg.MaxToolCalls)
	}
	if cfg.ThinkingIntensity != "" {
		appendScalar(agent, "thinking_intensity", cfg.ThinkingIntensity)
	}
	return nil
}

// SetVideoGen rewrites the top-level videogen: block. Credentials (api_key) use
// quoted-scalar style like provider creds. The block is small enough that
// comment loss inside videogen.* is acceptable.
func (f *File) SetVideoGen(cfg config.VideoGenConfig) error {
	root := f.root.Content[0]
	vg, _ := findValue(root, "videogen")
	if vg == nil {
		vg = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "videogen", vg)
	}
	if vg.Kind != yaml.MappingNode {
		return fmt.Errorf("persist: top-level videogen is not a mapping")
	}
	removeKey(vg, "providers")
	if len(cfg.Providers) == 0 {
		return nil
	}
	providers := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range sortedVideoProviderKeys(cfg.Providers) {
		p := cfg.Providers[name]
		pNode := &yaml.Node{Kind: yaml.MappingNode}
		appendQuotedScalar(pNode, "api_key", p.APIKey)
		if strings.TrimSpace(p.BaseURL) != "" {
			appendScalar(pNode, "base_url", p.BaseURL)
		}
		if len(p.Endpoints) > 0 {
			eps := &yaml.Node{Kind: yaml.MappingNode}
			for _, epName := range sortedVideoEndpointKeys(p.Endpoints) {
				epNode := &yaml.Node{Kind: yaml.MappingNode}
				appendScalar(epNode, "model", p.Endpoints[epName].Model)
				setKey(eps, epName, epNode)
			}
			setKey(pNode, "endpoints", eps)
		}
		setKey(providers, name, pNode)
	}
	setKey(vg, "providers", providers)
	return nil
}

func sortedVideoProviderKeys(m map[string]config.VideoProviderConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedVideoEndpointKeys(m map[string]config.VideoEndpointConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetToolConfig writes or replaces tools.<name>.* keys from raw.
// raw is the canonical wire-shape map for the tool (e.g. for
// web_search: enabled / api_key / limit). Keys absent from raw are
// deleted from yaml so the file always reflects the API caller's full
// intent — partial updates are the caller's responsibility (e.g.
// handler.go merges new values into the existing config struct before
// calling here).
//
// Creates the top-level `tools:` mapping and the `<name>:` child
// mapping if missing. Returns an error if name is empty or raw is
// nil. Unrelated tools (e.g. tools.bash.enabled) are not touched.
func (f *File) SetToolConfig(name string, raw map[string]any) error {
	if name == "" {
		return fmt.Errorf("persist.SetToolConfig: empty name")
	}
	if raw == nil {
		return fmt.Errorf("persist.SetToolConfig: nil config for %q", name)
	}
	tools, err := f.toolsNode(true /*create*/)
	if err != nil {
		return err
	}
	child, _ := findValue(tools, name)
	if child == nil || child.Kind != yaml.MappingNode {
		child = &yaml.Node{Kind: yaml.MappingNode}
		setKey(tools, name, child)
	}
	// Wipe existing keys; we always emit the caller's complete view.
	child.Content = child.Content[:0]
	emitToolField(child, "enabled", raw["enabled"])
	// Stable key order for review hygiene: enabled first, then the
	// remaining keys sorted by name.
	sorted := make([]string, 0, len(raw))
	for k := range raw {
		if k == "enabled" {
			continue
		}
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		emitToolField(child, k, raw[k])
	}
	forceBlockStyle(child)
	return nil
}

// toolsNode returns (or optionally creates) the top-level `tools:` mapping.
func (f *File) toolsNode(create bool) (*yaml.Node, error) {
	root := f.root.Content[0]
	tools, _ := findValue(root, "tools")
	if tools == nil {
		if !create {
			return nil, fmt.Errorf("tools block missing")
		}
		tools = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "tools", tools)
	}
	if tools.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("tools is not a mapping")
	}
	return tools, nil
}

// emitToolField writes one key/value into the tool's mapping with a
// type-appropriate scalar. Unsupported types (slices, nested maps) are
// silently skipped — the current tool configs don't use them. Boolean
// false / numeric zero / empty string are still emitted because
// callers may legitimately want to clear a credential. String values
// are written with double-quote style so credentials (api_key, etc.)
// survive round-trips through other yaml parsers that treat bare
// strings differently from quoted ones.
func emitToolField(m *yaml.Node, key string, v any) {
	switch val := v.(type) {
	case nil:
		// Skip — distinguish "field absent" from "field zeroed".
	case bool:
		appendBool(m, key, val)
	case string:
		appendQuotedScalar(m, key, val)
	case int:
		appendInt(m, key, val)
	case int64:
		appendInt(m, key, int(val))
	case float64:
		// JSON numbers decode as float64; check for integer values so
		// we don't write "5.0" where yaml expects "5".
		if val == float64(int(val)) {
			appendInt(m, key, int(val))
		} else {
			appendFloat(m, key, val)
		}
	}
}

// appendQuotedScalar appends a key/value pair with a double-quoted
// string scalar. Unlike appendScalar (which lets yaml.v3 choose the
// style), this forces DoubleQuotedStyle so values like credentials
// always appear as  key: "value"  in the file — consistent and
// unambiguous. Not tool-specific — usable wherever deterministic
// quoting is desired.
func appendQuotedScalar(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle},
	)
}

// SetProviderCreds writes or replaces the credentials block at
// llm.providers[name]. Preserves any existing nested `endpoints:`
// node — only the top-level credentials (type/base_url/api_key)
// are rewritten.
func (f *File) SetProviderCreds(name string, cfg config.ProviderConfig) error {
	provNode, _, err := f.providerNode(name, true /*create*/)
	if err != nil {
		return err
	}
	// Build a fresh creds block but copy over any existing endpoints
	// node so we don't drop nested config. Only carry forward a
	// proper MappingNode — a null/scalar `endpoints:` left over from
	// a previous marshal is discarded so it doesn't trip up future
	// SetEndpoint calls.
	existingEndpoints, _ := findValue(provNode, "endpoints")
	if existingEndpoints != nil && existingEndpoints.Kind != yaml.MappingNode {
		existingEndpoints = nil
	}

	// Wipe credential fields, leave endpoints alone.
	for _, k := range []string{"type", "base_url", "api_key", "disabled"} {
		removeKey(provNode, k)
	}
	if cfg.Type != "" {
		appendScalar(provNode, "type", cfg.Type)
	}
	if cfg.BaseURL != "" {
		appendScalar(provNode, "base_url", cfg.BaseURL)
	}
	if cfg.APIKey != "" {
		appendScalar(provNode, "api_key", cfg.APIKey)
	}
	// Only emit disabled when true (default false stays out of yaml).
	if cfg.Disabled {
		appendBool(provNode, "disabled", true)
	}
	if existingEndpoints != nil {
		setKey(provNode, "endpoints", existingEndpoints)
	}
	return nil
}

// SetEndpoint writes or replaces llm.providers[p].endpoints[e].
// Creates the parent provider entry and `endpoints` mapping if
// missing. Tolerates a degenerate `endpoints:` node that yaml has
// serialised as null/scalar (e.g. after every endpoint was deleted
// and yaml.v3 didn't emit `{}`) — replaces it with a fresh empty
// mapping rather than failing.
func (f *File) SetEndpoint(provName, epName string, ep config.EndpointConfig) error {
	provNode, _, err := f.providerNode(provName, true /*create*/)
	if err != nil {
		return err
	}
	endpoints, _ := findValue(provNode, "endpoints")
	if endpoints == nil || endpoints.Kind != yaml.MappingNode {
		// Either the key didn't exist, OR it exists but is null /
		// scalar (which yaml.v3 sometimes emits when a mapping was
		// empty at marshal time). Replace with a fresh mapping.
		endpoints = &yaml.Node{Kind: yaml.MappingNode}
		setKey(provNode, "endpoints", endpoints)
	}
	setKey(endpoints, epName, endpointToNode(ep))
	return nil
}

// RemoveEndpoint deletes llm.providers[p].endpoints[e] (no-op when
// absent). Errors if the parent provider doesn't exist. If the
// removal empties the endpoints mapping, the parent `endpoints:`
// key is removed entirely — keeps the yaml clean and avoids the
// "null endpoints" landmine for subsequent SetEndpoint calls.
func (f *File) RemoveEndpoint(provName, epName string) error {
	provNode, _, err := f.providerNode(provName, false)
	if err != nil {
		return err
	}
	if provNode == nil {
		return fmt.Errorf("persist: provider %q not found", provName)
	}
	endpoints, _ := findValue(provNode, "endpoints")
	if endpoints == nil {
		return nil
	}
	removeKey(endpoints, epName)
	if endpoints.Kind == yaml.MappingNode && len(endpoints.Content) == 0 {
		removeKey(provNode, "endpoints")
	}
	return nil
}

// llmNode returns (creating if needed) the `llm` mapping under root.
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

// providerNode returns the mapping node at llm.providers[name].
// If create=true and the entry is absent, a fresh mapping is added.
// If create=false and absent, returns (nil, -1, nil).
func (f *File) providerNode(name string, create bool) (*yaml.Node, int, error) {
	llm, err := f.llmNode()
	if err != nil {
		return nil, -1, err
	}
	providers, _ := findValue(llm, "providers")
	if providers == nil {
		if !create {
			return nil, -1, nil
		}
		providers = &yaml.Node{Kind: yaml.MappingNode}
		setKey(llm, "providers", providers)
	}
	if providers.Kind != yaml.MappingNode {
		return nil, -1, fmt.Errorf("persist: llm.providers is not a mapping")
	}
	provNode, idx := findValue(providers, name)
	if provNode == nil {
		if !create {
			return nil, -1, nil
		}
		provNode = &yaml.Node{Kind: yaml.MappingNode}
		setKey(providers, name, provNode)
	}
	if provNode.Kind != yaml.MappingNode {
		return nil, -1, fmt.Errorf("persist: llm.providers.%s is not a mapping", name)
	}
	return provNode, idx, nil
}

// endpointToNode builds a Mapping node from an EndpointConfig.
// Empty/zero fields are omitted.
//
// Field order matches the natural reading order: model → numeric
// tuning → enable_thinking.
func endpointToNode(ep config.EndpointConfig) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	if ep.Model != "" {
		appendScalar(n, "model", ep.Model)
	}
	if ep.MaxTokens != 0 {
		appendInt(n, "max_tokens", ep.MaxTokens)
	}
	if ep.Temperature != 0 {
		appendFloat(n, "temperature", ep.Temperature)
	}
	if ep.EnableThinking != nil {
		appendBool(n, "enable_thinking", *ep.EnableThinking)
	}
	if ep.ContextWindow != 0 {
		appendInt(n, "context_window", ep.ContextWindow)
	}
	// Only emit disabled when true to keep enabled endpoints' yaml
	// minimal (omitted = enabled is the default semantic).
	if ep.Disabled {
		appendBool(n, "disabled", true)
	}
	if len(ep.ModelType) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range ep.ModelType {
			seq.Content = append(seq.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: t,
			})
		}
		setKey(n, "model_type", seq)
	}
	// Group is emitted only when non-empty so existing endpoints stay
	// yaml-clean (omitted = "" by convention, same shape as ModelType).
	if ep.Group != "" {
		appendScalar(n, "group", ep.Group)
	}
	return n
}

// forceBlockStyle walks the AST and resets the Style field on every
// mapping / sequence node to 0 (block style). yaml.v3's emitter
// otherwise honours a node's parsed-in flow style, which compounds
// across round-trips: once any mapping has been written as
// `endpoints: {}` (or set FlowStyle by a manual edit), subsequent
// edits inherit and compact further. Forcing block on save keeps
// the file readable.
func forceBlockStyle(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = 0
	}
	for _, c := range n.Content {
		forceBlockStyle(c)
	}
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
// appended (preserving existing comment positioning).
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
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

func appendScalar(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func appendInt(m *yaml.Node, key string, value int) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)},
	)
}

func appendFloat(m *yaml.Node, key string, value float64) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: fmt.Sprintf("%g", value)},
	)
}

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
