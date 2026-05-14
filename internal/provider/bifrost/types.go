package bifrost

import (
	"sort"

	"github.com/maximhq/bifrost/core/schemas"
)

// allowedProviderTypes is the canonical mapping from user-facing
// `type` values (config.ProviderConfig.Type, API patches) to bifrost
// SDK ModelProvider constants. This is the SOURCE OF TRUTH —
// config validation, management API validation, and adapter
// construction all consult it.
//
// Additions: keep the user-facing name lowercase and match what the
// vendor calls itself (e.g. "openai" not "openai-chat-completions").
// Vendors with OpenAI-compatible HTTP APIs (DeepSeek, Moonshot/Kimi,
// Zhipu/GLM, MiniMax, etc.) are configured with type=openai plus a
// custom base_url — they aren't separate types here.
var allowedProviderTypes = map[string]schemas.ModelProvider{
	"openai":      schemas.OpenAI,
	"anthropic":   schemas.Anthropic,
	"gemini":      schemas.Gemini,
	"azure":       schemas.Azure,
	"bedrock":     schemas.Bedrock,
	"cohere":      schemas.Cohere,
	"vertex":      schemas.Vertex,
	"mistral":     schemas.Mistral,
	"ollama":      schemas.Ollama,
	"groq":        schemas.Groq,
	"openrouter":  schemas.OpenRouter,
	"perplexity":  schemas.Perplexity,
	"cerebras":    schemas.Cerebras,
	"huggingface": schemas.HuggingFace,
}

// ProviderTypeOf returns the bifrost ModelProvider mapped to the
// given user-facing type string, plus a flag indicating whether the
// type is known. Empty string returns ("", false).
func ProviderTypeOf(t string) (schemas.ModelProvider, bool) {
	p, ok := allowedProviderTypes[t]
	return p, ok
}

// AllowedTypeNames returns the sorted list of accepted type values.
// Used for error messages so operators see "expected one of [...]"
// without having to read source code.
func AllowedTypeNames() []string {
	names := make([]string, 0, len(allowedProviderTypes))
	for k := range allowedProviderTypes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
