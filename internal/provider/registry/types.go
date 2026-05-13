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
	Version     int                      `yaml:"version"       json:"version"`
	GeneratedAt time.Time                `yaml:"generated_at"  json:"generated_at"`
	Providers   map[string]*ProviderSpec `yaml:"providers"     json:"providers"`
	Models      map[string]*ModelSpec    `yaml:"models"        json:"models"`
}

// ProviderSpec captures everything about how to TALK to a vendor: where
// the endpoint lives, how to authenticate, and the quirks that diverge
// from a "plain OpenAI-compatible" baseline.
type ProviderSpec struct {
	DisplayName     string            `yaml:"display_name"       json:"display_name"`
	Family          string            `yaml:"family"             json:"family"`
	BaseURL         string            `yaml:"base_url"           json:"base_url"`
	Protocol        string            `yaml:"protocol"           json:"protocol"`
	Region          string            `yaml:"region"             json:"region"`
	Quirks          ProviderQuirks    `yaml:"quirks"             json:"quirks"`
	Auth            ProviderAuth      `yaml:"auth"               json:"auth"`
	Endpoints       ProviderEndpoints `yaml:"endpoints"          json:"endpoints"`
	ModelsDiscovery *ModelsDiscovery  `yaml:"models_discovery,omitempty" json:"models_discovery,omitempty"`
}

// ProviderQuirks declares deviations from the OpenAI/Anthropic baseline.
type ProviderQuirks struct {
	ThinkingParamStyle             string `yaml:"thinking_param_style"              json:"thinking_param_style"`
	ToolCallsRequireReasoningField bool   `yaml:"tool_calls_require_reasoning_field" json:"tool_calls_require_reasoning_field"`
	ExtraParamsPassthroughRequired bool   `yaml:"extra_params_passthrough_required" json:"extra_params_passthrough_required"`
	InlineUsageOnEveryChunk        bool   `yaml:"inline_usage_on_every_chunk"       json:"inline_usage_on_every_chunk"`
	ExplicitCacheControl           bool   `yaml:"explicit_cache_control"            json:"explicit_cache_control"`
	NeedsFirstByteTimeoutMs        int    `yaml:"needs_first_byte_timeout_ms"       json:"needs_first_byte_timeout_ms"`
}

// ProviderAuth describes how to attach credentials to outbound requests.
type ProviderAuth struct {
	Type      string `yaml:"type"       json:"type"`
	KeyHeader string `yaml:"key_header" json:"key_header"`
	KeyPrefix string `yaml:"key_prefix" json:"key_prefix"`
}

// ProviderEndpoints overrides individual endpoint paths.
type ProviderEndpoints struct {
	ChatCompletions string  `yaml:"chat_completions" json:"chat_completions"`
	ModelsList      string  `yaml:"models_list"      json:"models_list"`
	Embeddings      *string `yaml:"embeddings,omitempty" json:"embeddings,omitempty"`
}

// ModelsDiscovery describes whether the registry pulls live model lists.
// RefreshInterval is stored as a human-authored string ("1h", "30m") and
// parsed with time.ParseDuration at use time — yaml.v3 marshals
// time.Duration as raw int64 nanoseconds, which is unreadable in a
// hand-edited manifest.
type ModelsDiscovery struct {
	Mode            string `yaml:"mode"             json:"mode"`
	RefreshInterval string `yaml:"refresh_interval" json:"refresh_interval"`
}

// ModelSpec is one model's full capability profile.
type ModelSpec struct {
	Provider        string     `yaml:"provider"          json:"provider"`
	ModelID         string     `yaml:"model_id"          json:"model_id"`
	DisplayName     string     `yaml:"display_name"      json:"display_name"`
	Family          string     `yaml:"family"            json:"family"`
	Generation      string     `yaml:"generation,omitempty"        json:"generation,omitempty"`
	Deprecated      bool       `yaml:"deprecated,omitempty"        json:"deprecated,omitempty"`
	DeprecationDate *time.Time `yaml:"deprecation_date,omitempty"  json:"deprecation_date,omitempty"`
	KnowledgeCutoff string     `yaml:"knowledge_cutoff,omitempty"  json:"knowledge_cutoff,omitempty"`

	Modalities ModalitySpec  `yaml:"modalities" json:"modalities"`
	Supports   SupportsFlags `yaml:"supports"   json:"supports"`
	Limits     LimitsSpec    `yaml:"limits"     json:"limits"`
	Defaults   DefaultsSpec  `yaml:"defaults"   json:"defaults"`
	Metadata   ModelMetadata `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// ModalitySpec lists the input and output modalities.
type ModalitySpec struct {
	Input  []string `yaml:"input"  json:"input"`
	Output []string `yaml:"output" json:"output"`
}

// SupportsFlags is the capability matrix.
type SupportsFlags struct {
	Vision     bool `yaml:"vision"      json:"vision"`
	PDFInput   bool `yaml:"pdf_input"   json:"pdf_input"`
	AudioInput bool `yaml:"audio_input" json:"audio_input"`
	VideoInput bool `yaml:"video_input" json:"video_input"`

	AudioOutput     bool `yaml:"audio_output"     json:"audio_output"`
	ImageGeneration bool `yaml:"image_generation" json:"image_generation"`

	Streaming        bool `yaml:"streaming"         json:"streaming"`
	SystemMessages   bool `yaml:"system_messages"   json:"system_messages"`
	StructuredOutput bool `yaml:"structured_output" json:"structured_output"`

	FunctionCalling         bool `yaml:"function_calling"          json:"function_calling"`
	ParallelFunctionCalling bool `yaml:"parallel_function_calling" json:"parallel_function_calling"`
	ToolChoice              bool `yaml:"tool_choice"               json:"tool_choice"`
	ComputerUse             bool `yaml:"computer_use"              json:"computer_use"`
	WebSearch               bool `yaml:"web_search"                json:"web_search"`

	Reasoning             bool     `yaml:"reasoning"               json:"reasoning"`
	ReasoningCanDisable   bool     `yaml:"reasoning_can_disable"   json:"reasoning_can_disable"`
	ReasoningEffortLevels []string `yaml:"reasoning_effort_levels,omitempty" json:"reasoning_effort_levels,omitempty"`

	PromptCaching        bool `yaml:"prompt_caching"        json:"prompt_caching"`
	ExplicitCacheControl bool `yaml:"explicit_cache_control" json:"explicit_cache_control"`
}

// LimitsSpec captures the technical constraints.
type LimitsSpec struct {
	ContextWindow               int  `yaml:"context_window"    json:"context_window"`
	MaxInputTokens              int  `yaml:"max_input_tokens"  json:"max_input_tokens"`
	MaxOutputTokens             int  `yaml:"max_output_tokens" json:"max_output_tokens"`
	MaxReasoningTokens          *int `yaml:"max_reasoning_tokens,omitempty"      json:"max_reasoning_tokens,omitempty"`
	MaxToolCallsPerResponse     *int `yaml:"max_tool_calls_per_response,omitempty" json:"max_tool_calls_per_response,omitempty"`
	RequestTimeoutMsRecommended int  `yaml:"request_timeout_ms_recommended,omitempty" json:"request_timeout_ms_recommended,omitempty"`
}

// DefaultsSpec are pre-fill values.
type DefaultsSpec struct {
	Temperature            float64 `yaml:"temperature"             json:"temperature"`
	TopP                   float64 `yaml:"top_p"                   json:"top_p"`
	MaxOutputTokensDefault int     `yaml:"max_output_tokens_default" json:"max_output_tokens_default"`
}

// ModelMetadata is loose provenance / docs information.
type ModelMetadata struct {
	Source            string     `yaml:"source,omitempty"             json:"source,omitempty"`
	LastVerified      *time.Time `yaml:"last_verified,omitempty"      json:"last_verified,omitempty"`
	Notes             string     `yaml:"notes,omitempty"              json:"notes,omitempty"`
	UpstreamModelCard string     `yaml:"upstream_model_card,omitempty" json:"upstream_model_card,omitempty"`
}
