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
// Current scope: only the two backends actually exercised in
// production. Adding more is a one-line edit; revisit when an
// operator wants Gemini / Bedrock / etc. Vendors with OpenAI-
// compatible HTTP APIs (DeepSeek, Moonshot/Kimi, Zhipu/GLM, MiniMax,
// 讯飞, 通义, etc.) are configured with type=openai plus a custom
// base_url — they aren't separate types here.
var allowedProviderTypes = map[string]schemas.ModelProvider{
	"openai":    schemas.OpenAI,
	"anthropic": schemas.Anthropic,
	"gemini":    schemas.Gemini,
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
