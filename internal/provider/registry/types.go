// Package registry holds the model + provider metadata catalog used by the
// engine to drive provider-specific behavior (thinking, cache control,
// timeouts) and by the client UI to gate capability-dependent features
// (vision input, reasoning effort selectors, etc.).
//
// Pricing is intentionally excluded from this schema. Clients compute
// USD from raw token counts using their own pricing tables, consistent
// with the session metrics design decision (no server-side cost math).
package registry

import "time"

// Manifest is the top-level shape loaded from YAML. Map keys are stable
// identifiers used as foreign keys (ModelSpec.Provider must exist in
// Providers; Models keys take the form "{provider}/{model_id}").
type Manifest struct {
	Version     int                      `yaml:"version"`
	GeneratedAt time.Time                `yaml:"generated_at"`
	Providers   map[string]*ProviderSpec `yaml:"providers"`
	Models      map[string]*ModelSpec    `yaml:"models"`
}

// ProviderSpec captures everything about how to TALK to a vendor: where
// the endpoint lives, how to authenticate, and the quirks that diverge
// from a "plain OpenAI-compatible" baseline.
type ProviderSpec struct {
	DisplayName     string            `yaml:"display_name"`
	Family          string            `yaml:"family"`
	BaseURL         string            `yaml:"base_url"`
	Protocol        string            `yaml:"protocol"`
	Region          string            `yaml:"region"`
	Quirks          ProviderQuirks    `yaml:"quirks"`
	Auth            ProviderAuth      `yaml:"auth"`
	Endpoints       ProviderEndpoints `yaml:"endpoints"`
	ModelsDiscovery *ModelsDiscovery  `yaml:"models_discovery,omitempty"`
}

// ProviderQuirks declares deviations from the OpenAI/Anthropic baseline.
type ProviderQuirks struct {
	ThinkingParamStyle             string `yaml:"thinking_param_style"`
	ToolCallsRequireReasoningField bool   `yaml:"tool_calls_require_reasoning_field"`
	ExtraParamsPassthroughRequired bool   `yaml:"extra_params_passthrough_required"`
	InlineUsageOnEveryChunk        bool   `yaml:"inline_usage_on_every_chunk"`
	ExplicitCacheControl           bool   `yaml:"explicit_cache_control"`
	NeedsFirstByteTimeoutMs        int    `yaml:"needs_first_byte_timeout_ms"`
}

// ProviderAuth describes how to attach credentials to outbound requests.
type ProviderAuth struct {
	Type      string `yaml:"type"`
	KeyHeader string `yaml:"key_header"`
	KeyPrefix string `yaml:"key_prefix"`
}

// ProviderEndpoints overrides individual endpoint paths.
type ProviderEndpoints struct {
	ChatCompletions string  `yaml:"chat_completions"`
	ModelsList      string  `yaml:"models_list"`
	Embeddings      *string `yaml:"embeddings,omitempty"`
}

// ModelsDiscovery describes whether the registry pulls live model lists.
type ModelsDiscovery struct {
	Mode            string        `yaml:"mode"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

// ModelSpec is one model's full capability profile.
type ModelSpec struct {
	Provider        string     `yaml:"provider"`
	ModelID         string     `yaml:"model_id"`
	DisplayName     string     `yaml:"display_name"`
	Family          string     `yaml:"family"`
	Generation      string     `yaml:"generation,omitempty"`
	Deprecated      bool       `yaml:"deprecated,omitempty"`
	DeprecationDate *time.Time `yaml:"deprecation_date,omitempty"`
	KnowledgeCutoff string     `yaml:"knowledge_cutoff,omitempty"`

	Modalities ModalitySpec  `yaml:"modalities"`
	Supports   SupportsFlags `yaml:"supports"`
	Limits     LimitsSpec    `yaml:"limits"`
	Defaults   DefaultsSpec  `yaml:"defaults"`
	Metadata   ModelMetadata `yaml:"metadata,omitempty"`
}

// ModalitySpec lists the input and output modalities.
type ModalitySpec struct {
	Input  []string `yaml:"input"`
	Output []string `yaml:"output"`
}

// SupportsFlags is the capability matrix.
type SupportsFlags struct {
	Vision     bool `yaml:"vision"`
	PDFInput   bool `yaml:"pdf_input"`
	AudioInput bool `yaml:"audio_input"`
	VideoInput bool `yaml:"video_input"`

	AudioOutput     bool `yaml:"audio_output"`
	ImageGeneration bool `yaml:"image_generation"`

	Streaming        bool `yaml:"streaming"`
	SystemMessages   bool `yaml:"system_messages"`
	StructuredOutput bool `yaml:"structured_output"`

	FunctionCalling         bool `yaml:"function_calling"`
	ParallelFunctionCalling bool `yaml:"parallel_function_calling"`
	ToolChoice              bool `yaml:"tool_choice"`
	ComputerUse             bool `yaml:"computer_use"`
	WebSearch               bool `yaml:"web_search"`

	Reasoning             bool     `yaml:"reasoning"`
	ReasoningCanDisable   bool     `yaml:"reasoning_can_disable"`
	ReasoningEffortLevels []string `yaml:"reasoning_effort_levels,omitempty"`

	PromptCaching        bool `yaml:"prompt_caching"`
	ExplicitCacheControl bool `yaml:"explicit_cache_control"`
}

// LimitsSpec captures the technical constraints.
type LimitsSpec struct {
	ContextWindow               int  `yaml:"context_window"`
	MaxInputTokens              int  `yaml:"max_input_tokens"`
	MaxOutputTokens             int  `yaml:"max_output_tokens"`
	MaxReasoningTokens          *int `yaml:"max_reasoning_tokens,omitempty"`
	MaxToolCallsPerResponse     *int `yaml:"max_tool_calls_per_response,omitempty"`
	RequestTimeoutMsRecommended int  `yaml:"request_timeout_ms_recommended,omitempty"`
}

// DefaultsSpec are pre-fill values.
type DefaultsSpec struct {
	Temperature            float64 `yaml:"temperature"`
	TopP                   float64 `yaml:"top_p"`
	MaxOutputTokensDefault int     `yaml:"max_output_tokens_default"`
}

// ModelMetadata is loose provenance / docs information.
type ModelMetadata struct {
	Source            string    `yaml:"source,omitempty"`
	LastVerified      time.Time `yaml:"last_verified,omitempty"`
	Notes             string    `yaml:"notes,omitempty"`
	UpstreamModelCard string    `yaml:"upstream_model_card,omitempty"`
}
