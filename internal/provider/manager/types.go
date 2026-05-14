package manager

import "harnessclaw-go/internal/provider/failover"

// ProviderSnapshot is the API-facing view of a single provider
// configuration. APIKeyMask is the only field exposed publicly to
// avoid leaking secrets through the management endpoint.
type ProviderSnapshot struct {
	Name        string  `json:"name"`
	Model       string  `json:"model"`
	BaseURL     string  `json:"base_url"`
	APIKeyMask  string  `json:"api_key_mask"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature,omitempty"`
	// InChain reports whether the provider currently participates
	// in the failover chain. A provider can exist in
	// llm.providers without being in the chain (staged for later
	// activation).
	InChain bool `json:"in_chain"`
}

// ChainSnapshotPayload is the API-facing view of the active
// fallback chain plus live health state. Chain[i] corresponds to
// Entries[i] (same index).
type ChainSnapshotPayload struct {
	Chain   []string                  `json:"chain"`
	Entries []failover.ProviderHealth `json:"entries"`
}
