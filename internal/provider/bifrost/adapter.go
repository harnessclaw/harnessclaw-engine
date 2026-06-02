// Package bifrost provides a multi-backend LLM provider adapter using the
// Bifrost Go SDK (github.com/maximhq/bifrost/core).
//
// Bifrost abstracts multiple LLM providers behind a unified interface with:
//   - Multi-provider routing (Anthropic, OpenAI, Bedrock, Vertex, etc.)
//   - Built-in connection pooling, concurrency control, and request queuing
//   - Automatic key rotation and weighted load balancing
//   - Proxy support (HTTP, SOCKS5)
//   - Streaming via ChatCompletionStreamRequest
//
// The adapter implements provider.Provider so it can be used as a drop-in
// replacement for the direct Anthropic client.
package bifrost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/pkg/types"
)

// Config holds Bifrost adapter configuration.
type Config struct {
	// Provider selects the LLM backend (e.g. schemas.Anthropic, schemas.OpenAI).
	Provider schemas.ModelProvider

	// Model is the default model name (e.g. "claude-sonnet-4-20250514").
	Model string

	// APIKey is the provider API key.
	APIKey string

	// BaseURL overrides the provider's default endpoint (for proxies/gateways).
	// Empty string uses the provider's default.
	BaseURL string

	// FallbackModel is the model to use when the primary model is unavailable.
	// If empty, no fallback is configured.
	FallbackModel string

	// MaxConcurrency limits parallel requests. 0 uses Bifrost's default (1000).
	MaxConcurrency int

	// BufferSize is the request queue size. 0 uses Bifrost's default (5000).
	BufferSize int

	// ProxyURL sets an HTTP proxy for the provider. Empty means no proxy.
	ProxyURL string

	// CustomHeaders are forwarded to the provider on every request.
	CustomHeaders map[string]string

	// EnableThinking controls provider-side thinking-mode output.
	// nil   = don't send the field (provider default)
	// false = `enable_thinking: false` (DeepSeek v3.1+ extra param)
	// true  = `enable_thinking: true`
	// Forwarded via ChatParameters.ExtraParams so it reaches OpenAI-
	// compatible gateways without polluting the standard schema.
	EnableThinking *bool

	// DefaultTemperature is the sampling temperature used when the
	// incoming ChatRequest's Temperature is 0. Callers are expected to
	// pre-scale this into the target provider's legal range (caller
	// owns the range knowledge — e.g. main.buildBifrostAdapter scales
	// the agent-level [0,1] value by ×2 for openai/gemini). 0 means
	// "no default — only send Temperature when the request supplies it".
	DefaultTemperature float64

	// DefaultMaxTokens is the response cap used when the incoming
	// ChatRequest's MaxTokens is 0. Caller pre-caps this against the
	// endpoint's own MaxTokens so the value never exceeds endpoint
	// configuration. 0 disables the default.
	DefaultMaxTokens int

	// Quirks declares per-provider behavior deviations from the OpenAI
	// baseline (thinking param style, ExtraParams passthrough need,
	// tool_calls reasoning requirement, etc.). Sourced from the model
	// registry's ProviderSpec.Quirks. Nil leaves all defaults applied.
	Quirks *ProviderQuirks

	// Logger for operational events. Nil uses a no-op logger.
	Logger *zap.Logger
}

// ProviderQuirks is a runtime mirror of registry.ProviderQuirks used to
// drive adapter behavior. The cmd/server wires the two together at
// startup. The mirror exists so the bifrost package stays free of a
// dependency on internal/provider/registry (would create an import
// cycle if registry ever needs to call bifrost).
type ProviderQuirks struct {
	ThinkingParamStyle             string
	ToolCallsRequireReasoningField bool
	ExtraParamsPassthroughRequired bool
	InlineUsageOnEveryChunk        bool
	ExplicitCacheControl           bool
}

// account implements schemas.Account for Bifrost initialization.
type account struct {
	provider      schemas.ModelProvider
	apiKey        string
	model         string
	baseURL       string
	maxConcurrency int
	bufferSize    int
	proxyURL      string
}

func (a *account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{a.provider}, nil
}

func (a *account) GetKeysForProvider(_ context.Context, prov schemas.ModelProvider) ([]schemas.Key, error) {
	if prov != a.provider {
		return nil, fmt.Errorf("bifrost: unexpected provider %q, expected %q", prov, a.provider)
	}

	models := []string{}
	if a.model != "" {
		models = []string{a.model}
	}

	return []schemas.Key{{
		Value:  *schemas.NewEnvVar(a.apiKey),
		Models: models,
		Weight: 1.0,
	}}, nil
}

func (a *account) GetConfigForProvider(prov schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if prov != a.provider {
		return nil, fmt.Errorf("bifrost: unexpected provider %q, expected %q", prov, a.provider)
	}

	netCfg := schemas.DefaultNetworkConfig
	if a.baseURL != "" {
		netCfg.BaseURL = a.baseURL
	}
	// Increase stream idle timeout for agent workloads where the LLM may
	// "think" for extended periods (e.g. extended thinking, complex tool
	// orchestration) without sending any SSE chunks. The default 60s is
	// too aggressive for sub-agent tasks. 5 minutes accommodates heavy
	// reasoning while still catching truly dead connections.
	netCfg.StreamIdleTimeoutInSeconds = 300
	// DefaultRequestTimeoutInSeconds becomes fasthttp's ReadTimeout on the
	// underlying TCP connection. For streaming responses this must be at
	// least as long as the stream idle timeout, otherwise the connection-
	// level read deadline fires before the per-chunk idle timer, causing
	// "i/o timeout" errors on long LLM generations (e.g. writing reports).
	netCfg.DefaultRequestTimeoutInSeconds = 600

	concurrency := schemas.DefaultConcurrencyAndBufferSize
	if a.maxConcurrency > 0 {
		concurrency.Concurrency = a.maxConcurrency
	}
	if a.bufferSize > 0 {
		concurrency.BufferSize = a.bufferSize
	}

	cfg := &schemas.ProviderConfig{
		NetworkConfig:            netCfg,
		ConcurrencyAndBufferSize: concurrency,
	}

	if a.proxyURL != "" {
		cfg.ProxyConfig = &schemas.ProxyConfig{
			Type: schemas.HTTPProxy,
			URL:  a.proxyURL,
		}
	}

	return cfg, nil
}

// Adapter implements provider.Provider using the Bifrost SDK.
type Adapter struct {
	client        *bifrost.Bifrost
	providerKey   schemas.ModelProvider
	defaultModel  string
	fallbackModel string
	logger        *zap.Logger
	customHeaders map[string]string

	// enableThinking, when non-nil, is injected into every Chat request's
	// ExtraParams as `enable_thinking`. See Config.EnableThinking for
	// semantics. nil means "don't send the field".
	enableThinking *bool

	// quirks drives provider-specific behavior (thinking param style,
	// passthrough, reasoning placeholder). Nil means apply defaults.
	quirks *ProviderQuirks

	// defaultTemperature / defaultMaxTokens are agent-level defaults
	// applied when the incoming ChatRequest leaves the corresponding
	// field as zero. Caller (cmd/server.buildBifrostAdapter) is
	// responsible for pre-scaling temperature into the target
	// provider's legal range and pre-capping max_tokens against the
	// endpoint's own MaxTokens.
	defaultTemperature float64
	defaultMaxTokens   int

	mu            sync.Mutex
	usingFallback bool
	activeModel   string
}

// New creates a Bifrost adapter backed by the Bifrost SDK.
func New(cfg Config) (*Adapter, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("bifrost: APIKey is required")
	}
	if cfg.Provider == "" {
		cfg.Provider = schemas.Anthropic
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("bifrost: Model is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	acct := &account{
		provider:       cfg.Provider,
		apiKey:         cfg.APIKey,
		model:          cfg.Model,
		baseURL:        cfg.BaseURL,
		maxConcurrency: cfg.MaxConcurrency,
		bufferSize:     cfg.BufferSize,
		proxyURL:       cfg.ProxyURL,
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: acct,
	})
	if err != nil {
		return nil, fmt.Errorf("bifrost: init failed: %w", err)
	}

	return &Adapter{
		client:             client,
		providerKey:        cfg.Provider,
		defaultModel:       cfg.Model,
		fallbackModel:      cfg.FallbackModel,
		logger:             logger,
		customHeaders:      cfg.CustomHeaders,
		enableThinking:     cfg.EnableThinking,
		quirks:             cfg.Quirks,
		defaultTemperature: cfg.DefaultTemperature,
		defaultMaxTokens:   cfg.DefaultMaxTokens,
	}, nil
}

// Name returns "bifrost".
func (a *Adapter) Name() string { return "bifrost" }

// Chat sends a streaming chat request via the Bifrost SDK.
func (a *Adapter) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	model := a.currentModel()
	if req.Model != "" {
		model = req.Model
	}

	dialStart := time.Now()
	purpose := req.Purpose
	if purpose == "" {
		purpose = "<unset>"
	}
	a.logger.Debug("llm.call.dial",
		zap.String("provider", string(a.providerKey)),
		zap.String("model", model),
		zap.Int("messages", len(req.Messages)),
		zap.Int("tools", len(req.Tools)),
		zap.String("purpose", purpose),
	)

	bifReq := a.buildChatRequest(model, req)

	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	// ExtraParams passthrough: needed only for providers that rely on
	// non-OpenAI-standard parameters reaching the wire (DeepSeek
	// thinking, MiniMax wrappers, etc.). Without this flag the SDK
	// silently drops Params.ExtraParams during request marshalling (see
	// providers/utils/utils.go CheckContextAndGetRequestBody:1066).
	// Default-off keeps the request body canonical for providers that
	// don't need it.
	if a.quirks != nil && a.quirks.ExtraParamsPassthroughRequired {
		bfCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	}

	stream, bfErr := a.client.ChatCompletionStreamRequest(bfCtx, bifReq)
	if bfErr != nil {
		// On failure, try fallback if available.
		if a.fallbackModel != "" && !a.IsUsingFallback() {
			a.logger.Warn("bifrost: primary model failed, trying fallback",
				append([]zap.Field{
					zap.String("model", model),
					zap.String("fallback", a.fallbackModel),
					zap.String("error", bfErr.Error.Message),
					zap.Duration("dial_elapsed", time.Since(dialStart)),
				}, ctxStateFields(ctx)...)...,
			)
			a.mu.Lock()
			a.usingFallback = true
			a.mu.Unlock()

			bifReq.Model = a.fallbackModel
			stream, bfErr = a.client.ChatCompletionStreamRequest(bfCtx, bifReq)
			if bfErr != nil {
				a.logger.Warn("bifrost: fallback also failed",
					append([]zap.Field{
						zap.String("model", a.fallbackModel),
						zap.String("error", bfErr.Error.Message),
						zap.Duration("dial_elapsed", time.Since(dialStart)),
					}, ctxStateFields(ctx)...)...,
				)
				return nil, classifyBifrostError(bfErr, "bifrost: fallback also failed")
			}
		} else {
			// Pre-stream failure — log enough to tell whether bifrost saw our
			// ctx as already cancelled (engine-side) vs surfaced an upstream
			// network/HTTP error. The "Request cancelled: client disconnected"
			// bifrost message is ambiguous; ctx.Err() + context.Cause(ctx) at
			// this exact point disambiguates the two.
			a.logger.Warn("bifrost: stream request failed",
				append([]zap.Field{
					zap.String("model", model),
					zap.String("purpose", purpose),
					zap.String("error", bfErr.Error.Message),
					zap.Duration("dial_elapsed", time.Since(dialStart)),
					zap.Any("bfErr", formatBifrostError(bfErr)),
				}, ctxStateFields(ctx)...)...,
			)
			// On 4xx surface the full bifrost error structure AND a
			// shape-only dump of the outgoing request so we can spot
			// the request-body bug that triggered it (empty text
			// blocks, non-user first message, orphan tool_result, …).
			// 4xx is non-retryable — without this dump operators have
			// to reproduce just to see what the upstream complained
			// about. Body excerpt is 4kB capped to keep log volume sane.
			if bfErr.StatusCode != nil && *bfErr.StatusCode >= 400 && *bfErr.StatusCode < 500 {
				a.logger.Warn("bifrost: 4xx upstream rejection — full error + request shape",
					zap.Int("status", *bfErr.StatusCode),
					zap.String("model", model),
					zap.String("purpose", purpose),
					zap.ByteString("bfErr_full", marshalBifrostErrorFull(bfErr)),
					zap.ByteString("request_shape", marshalRequestShape(req)),
				)
			}
			return nil, classifyBifrostError(bfErr, "bifrost: stream request failed")
		}
	}

	a.logger.Debug("llm.call.connected",
		zap.String("model", model),
		zap.Duration("dial_elapsed", time.Since(dialStart)),
	)

	eventsCh := make(chan types.StreamEvent, 64)
	var streamErr error

	go func() {
		defer func() {
			fields := []zap.Field{
				zap.String("model", model),
				zap.Duration("total_elapsed", time.Since(dialStart)),
			}
			if streamErr != nil {
				// Mid-stream error: surface ctx state so we can tell
				// whether engine-side ctx was already cancelled (and
				// who cancelled it) when bifrost saw the failure.
				fields = append(fields, zap.String("stream_err", streamErr.Error()))
				fields = append(fields, ctxStateFields(ctx)...)
				a.logger.Warn("llm.call.stream_closed_with_error", fields...)
			} else {
				a.logger.Debug("llm.call.stream_closed", fields...)
			}
		}()
		defer close(eventsCh)
		streamErr = a.consumeStream(stream, eventsCh)
	}()

	return &provider.ChatStream{
		Events: eventsCh,
		Err:    func() error { return streamErr },
	}, nil
}

// CountTokens provides a rough token estimate (Bifrost SDK doesn't expose token counting).
func (a *Adapter) CountTokens(_ context.Context, msgs []types.Message) (int, error) {
	total := 0
	for _, m := range msgs {
		for _, cb := range m.Content {
			total += len(cb.Text) + len(cb.ToolInput) + len(cb.ToolResult)
		}
	}
	return total / 4, nil
}

// Shutdown gracefully shuts down the Bifrost client.
func (a *Adapter) Shutdown() {
	if a.client != nil {
		a.client.Shutdown()
	}
}

// SetModel overrides the model for subsequent requests.
func (a *Adapter) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeModel = model
	a.usingFallback = false
}

// ResetFallback clears the fallback state.
func (a *Adapter) ResetFallback() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usingFallback = false
}

// IsUsingFallback reports whether the adapter has switched to a fallback model.
func (a *Adapter) IsUsingFallback() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usingFallback
}

// ---------- Internal helpers ----------

// ctxStateFields returns the ctx.Err()/Cause snapshot at the call site.
// It exists to disambiguate bifrost's "Request cancelled: client disconnected"
// — which fires for ANY caller-side ctx cancellation — from genuine upstream
// failures. When ctx is still live, both fields will be present (ctx_err nil,
// ctx_cause nil) and the failure is genuinely upstream.
func ctxStateFields(ctx context.Context) []zap.Field {
	fields := []zap.Field{}
	if err := ctx.Err(); err != nil {
		fields = append(fields, zap.String("ctx_err", err.Error()))
	} else {
		fields = append(fields, zap.String("ctx_err", "<nil/live>"))
	}
	if cause := context.Cause(ctx); cause != nil && cause != ctx.Err() {
		fields = append(fields, zap.String("ctx_cause", cause.Error()))
	} else if cause := context.Cause(ctx); cause != nil {
		fields = append(fields, zap.String("ctx_cause", cause.Error()))
	} else {
		fields = append(fields, zap.String("ctx_cause", "<nil>"))
	}
	if dl, ok := ctx.Deadline(); ok {
		fields = append(fields, zap.Duration("ctx_deadline_in", time.Until(dl)))
	}
	return fields
}

// classifyBifrostError converts a Bifrost SDK error into a typed
// *retry.APIError so the engine-level Retryer can branch on Type /
// Retryable / StatusCode without scraping the formatted message text.
//
// Behaviour:
//   - bfErr.StatusCode set → retry.ClassifyHTTPError(code, ...)
//     (HTTP-level errors: 4xx → non-retryable, 5xx/429/529 → retryable)
//   - bfErr.StatusCode nil → retry.ClassifyNetworkError(...)
//     (network-level errors: connection reset / TLS / DNS — all retryable)
//
// prefix is prepended to the error message so call sites can distinguish
// "stream request failed" from "fallback also failed" without dropping
// the underlying Bifrost diagnostic.
func classifyBifrostError(bfErr *schemas.BifrostError, prefix string) *retry.APIError {
	msg := formatBifrostError(bfErr)
	if prefix != "" {
		msg = prefix + ": " + msg
	}
	if bfErr.StatusCode != nil {
		return retry.ClassifyHTTPError(*bfErr.StatusCode, msg, errors.New(msg))
	}
	return retry.ClassifyNetworkError(errors.New(msg))
}

// formatBifrostError builds a descriptive error string including HTTP status
// code, error type/code, and the underlying error when available.
func formatBifrostError(bfErr *schemas.BifrostError) string {
	msg := bfErr.Error.Message
	var parts []string
	if bfErr.StatusCode != nil {
		parts = append(parts, fmt.Sprintf("status=%d", *bfErr.StatusCode))
	}
	if bfErr.Error.Type != nil && *bfErr.Error.Type != "" {
		parts = append(parts, fmt.Sprintf("type=%s", *bfErr.Error.Type))
	}
	if bfErr.Error.Code != nil && *bfErr.Error.Code != "" {
		parts = append(parts, fmt.Sprintf("code=%s", *bfErr.Error.Code))
	}
	if bfErr.ExtraFields.Provider != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", bfErr.ExtraFields.Provider))
	}
	// Error.Error holds the underlying HTTP/network error with details
	// (e.g. status code, connection refused) that Error.Message lacks.
	if bfErr.Error.Error != nil && bfErr.Error.Error.Error() != msg {
		if len(parts) > 0 {
			return fmt.Sprintf("[%s] %s: %s", joinStrings(parts, " "), msg, bfErr.Error.Error.Error())
		}
		return fmt.Sprintf("%s: %s", msg, bfErr.Error.Error.Error())
	}
	if len(parts) > 0 {
		return fmt.Sprintf("[%s] %s", joinStrings(parts, " "), msg)
	}
	return msg
}

func joinStrings(parts []string, sep string) string {
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

// marshalBifrostErrorFull dumps the entire BifrostError struct as JSON
// so 4xx forensic logs surface every field the SDK populated — Type /
// Code / Message / Error.Error() / ExtraFields / Provider / RequestID /
// EventID — without relying on formatBifrostError's curated subset.
// Anthropic / OpenAI typically embed their JSON error body verbatim
// into bfErr.Error.Message; this is the only way to read it back.
// Returns a JSON byte slice; on marshal failure returns the literal
// "{}" so logging never panics.
func marshalBifrostErrorFull(bfErr *schemas.BifrostError) []byte {
	if bfErr == nil {
		return []byte("{}")
	}
	// Build a copy with the un-marshallable error.Error indirection
	// replaced by its string form. json.Marshal otherwise drops the
	// underlying error since `error` is an interface.
	type errCopy struct {
		Type    *string `json:"type,omitempty"`
		Code    *string `json:"code,omitempty"`
		Message string  `json:"message"`
		Cause   string  `json:"cause,omitempty"`
	}
	dump := struct {
		StatusCode  *int        `json:"status_code,omitempty"`
		IsBifrost   bool        `json:"is_bifrost_error,omitempty"`
		AllowRetry  bool        `json:"allow_retry,omitempty"`
		Error       errCopy     `json:"error"`
		ExtraFields any         `json:"extra_fields,omitempty"`
	}{
		StatusCode: bfErr.StatusCode,
		Error: errCopy{
			Type:    bfErr.Error.Type,
			Code:    bfErr.Error.Code,
			Message: bfErr.Error.Message,
		},
		ExtraFields: bfErr.ExtraFields,
	}
	if bfErr.Error.Error != nil {
		dump.Error.Cause = bfErr.Error.Error.Error()
	}
	b, err := json.Marshal(dump)
	if err != nil {
		return []byte("{}")
	}
	// 4kB cap — defensive against future huge upstream payloads. Logs
	// stay human-readable; longer payloads still answer the question.
	const cap = 4096
	if len(b) > cap {
		return append(b[:cap], []byte(`..."}}`)...)
	}
	return b
}

// marshalRequestShape produces a structural-only JSON of the outgoing
// request: per-message role, content-block types + a short text/tool
// preview, plus tool-schema names. NO full text bodies — operators
// don't need it and it inflates 4xx forensic logs by 100×. Targets
// "spot the empty block / orphan tool_result / non-user first message"
// scans, which is what we need right after a 400.
func marshalRequestShape(req *provider.ChatRequest) []byte {
	if req == nil {
		return []byte("{}")
	}
	type blockShape struct {
		Type     string `json:"type"`
		TextLen  int    `json:"text_len,omitempty"`
		Empty    bool   `json:"empty,omitempty"`
		ToolName string `json:"tool_name,omitempty"`
		ToolUseID string `json:"tool_use_id,omitempty"`
	}
	type msgShape struct {
		Index   int          `json:"index"`
		Role    string       `json:"role"`
		Tokens  int          `json:"tokens,omitempty"`
		Preview string       `json:"preview,omitempty"`
		Blocks  []blockShape `json:"blocks"`
	}
	shapes := make([]msgShape, 0, len(req.Messages))
	for i, m := range req.Messages {
		ms := msgShape{Index: i, Role: string(m.Role), Tokens: m.Tokens}
		var firstText string
		for _, b := range m.Content {
			bs := blockShape{Type: string(b.Type)}
			switch b.Type {
			case types.ContentTypeText:
				bs.TextLen = len(b.Text)
				bs.Empty = len(strings.TrimSpace(b.Text)) == 0
				if firstText == "" && len(b.Text) > 0 {
					if len(b.Text) > 80 {
						firstText = b.Text[:80] + "…"
					} else {
						firstText = b.Text
					}
				}
			case types.ContentTypeToolUse:
				bs.ToolName = b.ToolName
				bs.ToolUseID = b.ToolUseID
			case types.ContentTypeToolResult:
				bs.ToolName = b.ToolName
				bs.ToolUseID = b.ToolUseID
				bs.TextLen = len(b.ToolResult)
				bs.Empty = len(strings.TrimSpace(b.ToolResult)) == 0
			}
			ms.Blocks = append(ms.Blocks, bs)
		}
		ms.Preview = firstText
		shapes = append(shapes, ms)
	}
	toolNames := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Name)
	}
	out := struct {
		Purpose      string     `json:"purpose,omitempty"`
		Model        string     `json:"model,omitempty"`
		SystemLen    int        `json:"system_len"`
		MaxTokens    int        `json:"max_tokens,omitempty"`
		FirstRole    string     `json:"first_role,omitempty"`
		MsgCount     int        `json:"msg_count"`
		ToolCount    int        `json:"tool_count"`
		ToolNames    []string   `json:"tool_names,omitempty"`
		Messages     []msgShape `json:"messages"`
	}{
		Purpose:   req.Purpose,
		Model:     req.Model,
		SystemLen: len(req.System),
		MaxTokens: req.MaxTokens,
		MsgCount:  len(req.Messages),
		ToolCount: len(req.Tools),
		ToolNames: toolNames,
		Messages:  shapes,
	}
	if len(req.Messages) > 0 {
		out.FirstRole = string(req.Messages[0].Role)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("{}")
	}
	const cap = 8192
	if len(b) > cap {
		return append(b[:cap], []byte(`..."}`)...)
	}
	return b
}

func (a *Adapter) currentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.usingFallback && a.fallbackModel != "" {
		return a.fallbackModel
	}
	if a.activeModel != "" {
		return a.activeModel
	}
	return a.defaultModel
}

func (a *Adapter) buildChatRequest(model string, req *provider.ChatRequest) *schemas.BifrostChatRequest {
	// Skip replaying assistant reasoning_content when the operator
	// explicitly disabled thinking. Most providers don't require it on
	// the wire (verified against DeepSeek api.deepseek.com 2026-05-13:
	// both plain-text and tool-calls multi-turn replay without it).
	// Sending it back when disabled just inflates input tokens by the
	// length of every prior turn's chain-of-thought, defeating the
	// point of disabling thinking.
	includeReasoning := a.enableThinking == nil || *a.enableThinking
	if a.quirks != nil && a.quirks.ToolCallsRequireReasoningField && !includeReasoning {
		// Provider's schema (DeepSeek thinking-mode) demands the
		// reasoning_content field on tool_calls assistant messages even
		// when thinking is disabled. Keep the wire-level placeholder
		// flowing; convertSingleMessage only emits "" content, not the
		// actual reasoning history.
		includeReasoning = true
	}
	bifReq := &schemas.BifrostChatRequest{
		Provider: a.providerKey,
		Model:    model,
		Input:    convertMessages(req.Messages, req.System, includeReasoning, a.logger),
	}

	params := a.buildParams(req)
	if a.enableThinking != nil {
		style := ""
		if a.quirks != nil {
			style = a.quirks.ThinkingParamStyle
		}
		switch style {
		case "deepseek_type":
			// DeepSeek: extra_params.thinking = {"type": "enabled"|"disabled"}.
			// See https://api-docs.deepseek.com/zh-cn/guides/thinking_mode .
			// Verified 2026-05-13 against deepseek-v4-flash: setting
			// `thinking.type=disabled` actually suppresses reasoning_tokens
			// (the bare `enable_thinking: false` field is silently
			// ignored). Forwarded via ExtraParams.
			if params == nil {
				params = &schemas.ChatParameters{}
			}
			if params.ExtraParams == nil {
				params.ExtraParams = make(map[string]interface{})
			}
			typeStr := "disabled"
			if *a.enableThinking {
				typeStr = "enabled"
			}
			params.ExtraParams["thinking"] = map[string]string{"type": typeStr}
		case "openai_effort":
			// OpenAI o-series reasoning models: params.reasoning.effort.
			if params == nil {
				params = &schemas.ChatParameters{}
			}
			eff := "none"
			if *a.enableThinking {
				eff = "medium"
			}
			params.Reasoning = &schemas.ChatReasoning{Effort: &eff}
		case "anthropic_budget":
			// Anthropic Claude extended thinking: reasoning.max_tokens
			// (only when enabled — no off-equivalent on the wire).
			if *a.enableThinking {
				if params == nil {
					params = &schemas.ChatParameters{}
				}
				budget := 4096
				params.Reasoning = &schemas.ChatReasoning{MaxTokens: &budget}
			}
		case "openrouter":
			// OpenRouter aggregator: reasoning.enabled boolean toggle.
			if params == nil {
				params = &schemas.ChatParameters{}
			}
			enabled := *a.enableThinking
			params.Reasoning = &schemas.ChatReasoning{Enabled: &enabled}
		case "", "none":
			// Provider doesn't support a thinking toggle — silently noop.
		}
	}
	if params != nil {
		bifReq.Params = params
	}

	if a.fallbackModel != "" {
		bifReq.Fallbacks = []schemas.Fallback{{
			Model: a.fallbackModel,
		}}
	}

	return bifReq
}

// consumeStream reads from the Bifrost stream channel and emits types.StreamEvent values.
//
// We return as soon as the upstream emits a chunk with FinishReason set —
// the Bifrost SDK's chunk channel does NOT close promptly after the final
// chunk; it stays open until the underlying HTTP socket hits its idle
// timeout (~400s for OpenAI). Without this early return, every call
// "hangs" for 6m40s after the model finishes, even though MessageEnd was
// already emitted upstream and the client got the answer in seconds.
// See logs around 2026-05-06 for the smoking gun (tail_after_last_chunk
// ~6m40s consistently across runs).
func (a *Adapter) consumeStream(stream chan *schemas.BifrostStreamChunk, out chan<- types.StreamEvent) error {
	var toolCalls []toolCallAccumulator

	// Bifrost's OpenAI provider for chat streams (see core/providers/openai/openai.go
	// HandleOpenAIChatCompletionStreaming) accumulates usage from every chunk into a
	// goroutine-local variable, clears `response.Usage = nil` on each chunk it
	// forwards, then — after the upstream channel closes (or [DONE] arrives) —
	// emits ONE extra synthetic chunk carrying the aggregated usage. That
	// synthetic chunk has NO choices (or empty delta) and NO finish_reason.
	//
	// We therefore can't emit MessageEnd the moment we see finish_reason on
	// a per-content chunk: that chunk's Usage is always nil (bifrost cleared it),
	// and the real usage rides on a later chunk we'd miss with an early return.
	//
	// Strategy: stash finish_reason + model when seen; keep reading until the
	// stream closes OR we receive a usage-bearing chunk. Emit MessageEnd
	// exactly once on close (or on first usage-bearing chunk after we've seen
	// a finish_reason). Tool-call deltas accumulated across the run are flushed
	// just before MessageEnd.
	var pendingStopReason, pendingModel string
	var pendingFinish bool
	var pendingUsage *types.Usage
	var reasoningBuf strings.Builder

	// Per-stream event histogram. Counts the SHAPE of every upstream
	// chunk so MessageEnd can publish what actually arrived — without
	// this we cannot explain "completion_tokens=56 but text_chars=0":
	// the 56 tokens must have ridden as a chunk type we ignored (no
	// content + no tool_call + no reasoning + no finish_reason). A
	// non-zero `chunks_other` on MessageEnd is the signal we silently
	// dropped real model output. Counters are local to this stream;
	// no cross-call aggregation.
	var (
		chunksTotal       int // every non-nil, non-error chunk we processed
		chunksUsageOnly   int // pure usage chunk (no choices)
		chunksTextDelta   int // delta.Content non-empty
		chunksReasoning   int // delta.Reasoning non-empty
		chunksToolCalls   int // delta.ToolCalls non-empty
		chunksFinish      int // FinishReason set on the chunk
		chunksOther       int // had Choices but produced no signal we forward
	)

	flushToolCalls := func() {
		for _, tc := range toolCalls {
			out <- types.StreamEvent{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{
					ID:    tc.id,
					Name:  tc.name,
					Input: tc.args.String(),
				},
			}
		}
		toolCalls = nil
	}

	emitMessageEnd := func() {
		flushToolCalls()
		reasoning := reasoningBuf.String()
		// Always publish the stream-shape histogram so MessageEnd can
		// answer "where did the completion tokens go?". Cheap (six ints)
		// and the only way to detect silently-dropped upstream channels.
		histFields := []zap.Field{
			zap.Int("chunks_total", chunksTotal),
			zap.Int("chunks_text_delta", chunksTextDelta),
			zap.Int("chunks_tool_call_delta", chunksToolCalls),
			zap.Int("chunks_reasoning_delta", chunksReasoning),
			zap.Int("chunks_finish", chunksFinish),
			zap.Int("chunks_usage_only", chunksUsageOnly),
			zap.Int("chunks_other", chunksOther),
		}
		if a.logger != nil {
			if pendingUsage != nil {
				a.logger.Debug("bifrost stream MessageEnd",
					append([]zap.Field{
						zap.String("model", pendingModel),
						zap.Bool("has_usage", true),
						zap.Int("input_tokens", pendingUsage.InputTokens),
						zap.Int("output_tokens", pendingUsage.OutputTokens),
						zap.Int("cache_read", pendingUsage.CacheRead),
						zap.Int("thinking", pendingUsage.ThinkingTokens),
						zap.Int("reasoning_chars", len(reasoning)),
					}, histFields...)...,
				)
				// Smoking-gun guard: usage says the model generated
				// tokens, but none of our forwarded channels caught
				// anything. Warn so it leaves a unique fingerprint in
				// the log even when surrounding INFO is quiet.
				if pendingUsage.OutputTokens > 0 &&
					chunksTextDelta == 0 && chunksToolCalls == 0 &&
					chunksReasoning == 0 && pendingUsage.ThinkingTokens == 0 {
					a.logger.Warn("bifrost stream: output_tokens > 0 but no text/tool/reasoning deltas — upstream returned a channel we don't forward",
						append([]zap.Field{
							zap.String("model", pendingModel),
							zap.Int("output_tokens", pendingUsage.OutputTokens),
							zap.String("stop_reason", pendingStopReason),
						}, histFields...)...,
					)
				}
			} else {
				a.logger.Warn("bifrost stream MessageEnd without usage",
					append([]zap.Field{
						zap.String("model", pendingModel),
						zap.Bool("has_usage", false),
						zap.Int("reasoning_chars", len(reasoning)),
					}, histFields...)...,
				)
			}
		}
		stopReason := pendingStopReason
		if stopReason == "" {
			stopReason = "end_turn"
		}
		out <- types.StreamEvent{
			Type:       types.StreamEventMessageEnd,
			StopReason: stopReason,
			Usage:      pendingUsage,
			Model:      pendingModel,
			Reasoning:  reasoning,
		}
	}

	for chunkPtr := range stream {
		if chunkPtr == nil {
			continue
		}
		chunk := *chunkPtr

		if chunk.BifrostError != nil {
			apiErr := classifyBifrostError(chunk.BifrostError, "bifrost stream error")
			out <- types.StreamEvent{
				Type:  types.StreamEventError,
				Error: apiErr,
			}
			return apiErr
		}

		if chunk.BifrostChatResponse == nil {
			continue
		}
		chunksTotal++

		// Capture model name on the first chunk that reports one.
		if pendingModel == "" && chunk.BifrostChatResponse.Model != "" {
			pendingModel = chunk.BifrostChatResponse.Model
		}

		// Capture usage whenever a chunk carries it. Bifrost's chat path
		// forwards usage only on the synthetic final chunk (no choices,
		// no finish_reason), but other providers / future versions may
		// inline it on the same chunk that carries finish_reason; take
		// the first non-nil usage we see and keep processing the chunk
		// so a same-chunk finish_reason isn't dropped.
		if chunk.BifrostChatResponse.Usage != nil && pendingUsage == nil {
			u := chunk.BifrostChatResponse.Usage

			// Verbose debug: dump the raw BifrostLLMUsage as JSON so
			// operators can see exactly what the provider returned
			// (including provider-specific subfields like
			// prompt_tokens_details.cached_tokens, reasoning_tokens,
			// cache_creation_input_tokens etc.). Costs one Marshal per
			// LLM call, gated on Debug level so production runs at
			// INFO see nothing.
			if a.logger != nil && a.logger.Core().Enabled(zap.DebugLevel) {
				if raw, err := json.Marshal(u); err == nil {
					a.logger.Debug("bifrost raw usage",
						zap.String("model", chunk.BifrostChatResponse.Model),
						zap.ByteString("usage_json", raw),
					)
				}
			}

			pendingUsage = &types.Usage{
				InputTokens:  u.PromptTokens,
				OutputTokens: u.CompletionTokens,
			}
			if u.PromptTokensDetails != nil {
				pendingUsage.CacheRead = u.PromptTokensDetails.CachedReadTokens
				pendingUsage.CacheWrite = u.PromptTokensDetails.CachedWriteTokens
			}
			if u.CompletionTokensDetails != nil {
				pendingUsage.ThinkingTokens = u.CompletionTokensDetails.ReasoningTokens
			}
		}

		if len(chunk.BifrostChatResponse.Choices) == 0 {
			// Pure usage-only synthetic chunk (bifrost's chat wrap-up).
			// If finish_reason already arrived earlier, this is the last
			// piece we needed — emit and exit. Otherwise keep reading,
			// finish_reason may still be coming.
			chunksUsageOnly++
			if pendingFinish && pendingUsage != nil {
				emitMessageEnd()
				return nil
			}
			continue
		}
		choice := chunk.BifrostChatResponse.Choices[0]

		// Streaming delta.
		chunkHadSignal := false
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			delta := choice.ChatStreamResponseChoice.Delta

			if delta.Content != nil && *delta.Content != "" {
				chunksTextDelta++
				chunkHadSignal = true
				out <- types.StreamEvent{
					Type: types.StreamEventText,
					Text: *delta.Content,
				}
			}

			// Accumulate thinking-mode reasoning (DeepSeek / OpenAI o*
			// / xAI all surface it via delta.Reasoning — bifrost SDK
			// also maps DeepSeek's `reasoning_content` alias here).
			// We don't stream it to the wire (UI doesn't render it
			// today). When the operator has explicitly disabled
			// thinking we drop the content immediately so it never
			// hits the assistant Message and inflates input tokens
			// on replay. The reasoning_content FIELD is still echoed
			// back on tool_call assistant turns (see convertSingleMessage)
			// with an empty string, which DeepSeek's strict validator
			// requires but costs zero tokens.
			thinkingEnabled := a.enableThinking == nil || *a.enableThinking
			if thinkingEnabled && delta.Reasoning != nil && *delta.Reasoning != "" {
				chunksReasoning++
				chunkHadSignal = true
				reasoningBuf.WriteString(*delta.Reasoning)
			}

			if len(delta.ToolCalls) > 0 {
				chunksToolCalls++
				chunkHadSignal = true
				for _, tc := range delta.ToolCalls {
					toolCalls = accumulateToolCall(toolCalls, tc)
				}
			}
		}

		// finish_reason: stash it and keep reading. The usage-bearing
		// synthetic chunk (see header comment) lands AFTER this point on
		// the bifrost chat path, so an early return here would lose it.
		if choice.FinishReason != nil {
			chunksFinish++
			chunkHadSignal = true
			pendingFinish = true
			pendingStopReason = mapFinishReason(*choice.FinishReason)

			// If usage was already captured (rare: provider inlined it
			// on or before this chunk), emit and return now.
			if pendingUsage != nil {
				emitMessageEnd()
				return nil
			}
		}
		// Track chunks that had Choices but produced nothing we forward —
		// a non-zero count at MessageEnd means the upstream sent payload
		// on a channel we silently ignored (refusal text / annotations /
		// future provider quirk). The Q1-style "56 tokens of nothing"
		// fingerprint we hit on 2026-06-02 lives here.
		if !chunkHadSignal {
			chunksOther++
		}
	}

	// Stream closed without a usage-bearing final chunk. Emit whatever we
	// have so the engine never hangs waiting on MessageEnd.
	if pendingFinish || len(toolCalls) > 0 {
		emitMessageEnd()
	}
	return nil
}

// ---------- Message conversion ----------

// convertMessages transforms internal messages to Bifrost ChatMessage format.
// includeReasoning controls whether prior assistant ReasoningContent is
// echoed back on the wire — disable to avoid input-token inflation when
// the provider doesn't require it (most don't).
func convertMessages(msgs []types.Message, systemPrompt string, includeReasoning bool, logger *zap.Logger) []schemas.ChatMessage {
	var result []schemas.ChatMessage

	// System prompt as a system message.
	if systemPrompt != "" {
		result = append(result, schemas.ChatMessage{
			Role: schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{
				ContentStr: schemas.Ptr(systemPrompt),
			},
		})
	}

	for _, msg := range msgs {
		if msg.Role == types.RoleSystem {
			// Already handled above or skip.
			continue
		}

		bfMsg := convertSingleMessage(msg, includeReasoning)
		if bfMsg != nil {
			result = append(result, *bfMsg)
		}
	}
	// Drop orphan tool messages — a "tool" role entry whose tool_call_id
	// has no matching tool_call on a preceding assistant message. OpenAI
	// (and DeepSeek's OpenAI-compatible endpoint) reject the request
	// outright with `Messages with role 'tool' must be a response to a
	// preceding message with 'tool_calls'`. Orphans arise when:
	//   - the compactor's keep-tail boundary lands on a tool_result whose
	//     producing assistant got summarised away (compactor advances
	//     past these now, but a stale prefix from before the fix can
	//     still surface);
	//   - ContractEnforcer-style hooks inject a synthetic tool_result for
	//     a tool_use_id that never appeared on an assistant message;
	//   - a sub-agent session is reloaded after partial truncation.
	// Letting the request fail strands the entire loop turn — better to
	// drop the orphan and log it so the conversation continues.
	result = sanitizeToolSequence(result, logger)
	// Trim cache_control breakpoints to Anthropic's per-request limit.
	// Per-block cache_control is added eagerly in convertSingleMessage;
	// this is a post-conversion clamp so a long multi-image
	// conversation doesn't blow the 4-breakpoint cap and 400 from
	// upstream.
	capImageCacheBreakpoints(result)
	return result
}

// sanitizeToolSequence enforces the OpenAI/DeepSeek constraint that a
// `tool` role message must be the immediate response to an assistant
// message that opened the matching tool_call. It does two things:
//
//  1. Build the set of tool_call_ids OPENED by all preceding assistant
//     tool_calls in the slice — `openCalls`.
//  2. Walk the slice. For each `tool` message, drop it if its
//     tool_call_id isn't in openCalls.
//  3. After dropping, scan ASSISTANT messages from the OTHER direction:
//     if an assistant has tool_calls but the IMMEDIATELY-FOLLOWING
//     messages don't cover every tool_call_id with a tool response,
//     the wire request is still invalid (OpenAI: "Every tool_call must
//     have a matching tool response"). Strip those unanswered tool_calls
//     from the assistant message (keep its text/reasoning so the
//     assistant turn isn't lost entirely).
//
// Logged at debug with shape info so we can see the orphan source
// without reproducing the trace.
func sanitizeToolSequence(msgs []schemas.ChatMessage, logger *zap.Logger) []schemas.ChatMessage {
	if len(msgs) == 0 {
		return msgs
	}
	// Pass 1: collect every tool_call_id any assistant ever opened.
	openCalls := make(map[string]bool)
	for i := range msgs {
		m := &msgs[i]
		if m.Role == schemas.ChatMessageRoleAssistant && m.ChatAssistantMessage != nil {
			for _, tc := range m.ChatAssistantMessage.ToolCalls {
				if tc.ID != nil && *tc.ID != "" {
					openCalls[*tc.ID] = true
				}
			}
		}
	}

	// Pass 2: drop tool messages whose call_id isn't in openCalls, and
	// record which call_ids actually got answered.
	answered := make(map[string]bool)
	out := make([]schemas.ChatMessage, 0, len(msgs))
	droppedToolIDs := []string{}
	for i := range msgs {
		m := &msgs[i]
		if m.Role == schemas.ChatMessageRoleTool {
			id := ""
			if m.ChatToolMessage != nil && m.ChatToolMessage.ToolCallID != nil {
				id = *m.ChatToolMessage.ToolCallID
			}
			if id == "" || !openCalls[id] {
				droppedToolIDs = append(droppedToolIDs, id)
				continue
			}
			answered[id] = true
		}
		out = append(out, *m)
	}

	// Pass 3: for any assistant with tool_calls that didn't all get a
	// response (the SECOND failure mode orphan-only dropping misses),
	// strip the unanswered tool_calls. Skip the LAST assistant in the
	// slice — when loop.Run sends the request right after the model
	// emitted tool_calls but before tool dispatch (which is what every
	// LLM call does), the tail assistant message legitimately has
	// "pending" tool_calls and the next message will be the tool
	// results. Only ASSISTANT-WITH-TOOL_CALLS that have more messages
	// after them in the slice without a matching tool response are
	// truly broken.
	strippedFromAssistants := 0
	emptiedAssistants := 0
	final := make([]schemas.ChatMessage, 0, len(out))
	lastAssistantIdx := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == schemas.ChatMessageRoleAssistant {
			lastAssistantIdx = i
			break
		}
	}
	for i := range out {
		m := &out[i]
		if i != lastAssistantIdx &&
			m.Role == schemas.ChatMessageRoleAssistant &&
			m.ChatAssistantMessage != nil &&
			len(m.ChatAssistantMessage.ToolCalls) > 0 {
			kept := m.ChatAssistantMessage.ToolCalls[:0]
			for _, tc := range m.ChatAssistantMessage.ToolCalls {
				if tc.ID != nil && answered[*tc.ID] {
					kept = append(kept, tc)
				} else {
					strippedFromAssistants++
				}
			}
			m.ChatAssistantMessage.ToolCalls = kept
			if len(kept) == 0 {
				// Drop empty wrapper if the message had no text either.
				hasText := false
				if m.Content != nil {
					if m.Content.ContentStr != nil && *m.Content.ContentStr != "" {
						hasText = true
					}
					for _, b := range m.Content.ContentBlocks {
						if b.Type == schemas.ChatContentBlockTypeText && b.Text != nil && *b.Text != "" {
							hasText = true
							break
						}
					}
				}
				if !hasText {
					emptiedAssistants++
					continue
				}
				// Has text → keep message but clear the assistant wrapper
				// so the wire doesn't see an empty `tool_calls` array.
				m.ChatAssistantMessage = nil
			}
		}
		final = append(final, *m)
	}

	// Pass 4: repair truncated tool_call.Arguments. When max_tokens cuts
	// a tool_use mid-stream, the stream-end accumulator emits a partial
	// JSON string like `{"file_path":"a","content":"const {\n  Document`.
	// Downstream, bifrost-core/providers/anthropic/chat.go:380-387
	// fallbacks that partial blob into a json.RawMessage when its
	// compactJSONBytes round-trip fails — and json.RawMessage's
	// MarshalJSON returns the bytes verbatim, which encoding/json then
	// rejects with `invalid Marshaler output json syntax at N`. The
	// entire anthropic request body fails to marshal, the stream dies
	// with a non-retryable error, and the whole sub-agent crashes —
	// even though the offending message is several turns back in
	// history. Rewrite to "{}" preserves the (id, name) pair so the
	// matching tool_result (already carrying a truncation error) stays
	// addressable; the LLM sees the truncation hint on the tool_result
	// side and is told not to retry. Empty Arguments is left as-is
	// (some providers send no arguments for zero-arg tools).
	repairedToolCalls := 0
	for i := range final {
		m := &final[i]
		if m.Role != schemas.ChatMessageRoleAssistant || m.ChatAssistantMessage == nil {
			continue
		}
		for j := range m.ChatAssistantMessage.ToolCalls {
			args := m.ChatAssistantMessage.ToolCalls[j].Function.Arguments
			if args == "" {
				continue
			}
			if json.Valid([]byte(args)) {
				continue
			}
			m.ChatAssistantMessage.ToolCalls[j].Function.Arguments = "{}"
			repairedToolCalls++
		}
	}

	if logger != nil && (len(droppedToolIDs) > 0 || strippedFromAssistants > 0 || emptiedAssistants > 0 || repairedToolCalls > 0) {
		logger.Warn("bifrost: sanitized tool message sequence",
			zap.Int("in", len(msgs)),
			zap.Int("out", len(final)),
			zap.Strings("dropped_orphan_tool_ids", droppedToolIDs),
			zap.Int("stripped_unanswered_tool_calls", strippedFromAssistants),
			zap.Int("emptied_assistants", emptiedAssistants),
			zap.Int("repaired_truncated_tool_calls", repairedToolCalls),
		)
	}
	return final
}

// maxCacheControlBreakpoints is Anthropic's per-request hard limit on
// cache_control entries. Exceeding it returns a 400 from the API.
// Other providers (OpenAI, Gemini) ignore cache_control entirely, so
// the cap is purely an Anthropic concern but applied universally —
// the field is dropped quietly downstream when not honored.
const maxCacheControlBreakpoints = 4

// capImageCacheBreakpoints enforces the per-request breakpoint limit
// by stripping cache_control from the OLDEST excess image / file
// blocks first. The most recent N stay cached (those are most likely
// to be referenced in follow-up turns and worth the cache_write).
//
// Mutates the slice in place via pointer chasing into the underlying
// ContentBlock arrays. Safe because we don't grow / re-slice anything;
// only flip the CacheControl pointer to nil.
func capImageCacheBreakpoints(msgs []schemas.ChatMessage) {
	type ref struct {
		mi int
		bi int
	}
	var refs []ref
	for mi := range msgs {
		c := msgs[mi].Content
		if c == nil || c.ContentBlocks == nil {
			continue
		}
		for bi := range c.ContentBlocks {
			if c.ContentBlocks[bi].CacheControl != nil {
				refs = append(refs, ref{mi: mi, bi: bi})
			}
		}
	}
	if len(refs) <= maxCacheControlBreakpoints {
		return
	}
	drop := len(refs) - maxCacheControlBreakpoints
	for i := 0; i < drop; i++ {
		msgs[refs[i].mi].Content.ContentBlocks[refs[i].bi].CacheControl = nil
	}
}

func convertSingleMessage(msg types.Message, includeReasoning bool) *schemas.ChatMessage {
	role := mapRole(msg.Role)

	// Check if this is a tool result message.
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolResult {
			return &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(cb.ToolResult),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: &cb.ToolUseID,
				},
			}
		}
	}

	// Build content blocks.
	var blocks []schemas.ChatContentBlock
	var toolCalls []schemas.ChatAssistantMessageToolCall

	for _, cb := range msg.Content {
		switch cb.Type {
		case types.ContentTypeText:
			if cb.Text != "" {
				blocks = append(blocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: schemas.Ptr(cb.Text),
				})
			}
		case types.ContentTypeToolUse:
			tc := schemas.ChatAssistantMessageToolCall{
				ID:   &cb.ToolUseID,
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      &cb.ToolName,
					Arguments: cb.ToolInput,
				},
			}
			toolCalls = append(toolCalls, tc)
		case types.ContentTypeImage:
			// Bifrost's anthropic provider (utils.go:1077
			// ConvertToAnthropicImageBlock) handles both inline base64
			// data URLs and remote URLs. OpenAI's provider accepts the
			// same shape via the OpenAI Vision spec. We hand the SDK a
			// fully-formed URL: either the caller's https:// URL or a
			// data: URL synthesized from base64 + media_type. The SDK
			// then re-extracts media_type via the data: prefix when
			// the target provider needs it (Anthropic does).
			url := cb.URL
			if url == "" && cb.Data != "" {
				url = "data:" + cb.MediaType + ";base64," + cb.Data
			}
			if url == "" {
				// No data + no URL ⇒ drop silently. Builder + Gate
				// should have caught this upstream; defense-in-depth
				// for legacy callers or future bugs.
				continue
			}
			blocks = append(blocks, schemas.ChatContentBlock{
				Type:           schemas.ChatContentBlockTypeImage,
				ImageURLStruct: &schemas.ChatInputImage{URL: url},
				// Anthropic prompt-cache breakpoint. Images are the
				// largest stable token chunks in multimodal turns
				// (~1500-1600 tokens per image); without this, every
				// follow-up turn re-pays the full image tokens.
				// Bifrost's anthropic provider serializes this to
				// cache_control:{"type":"ephemeral"}; other providers
				// ignore the field (it's an Anthropic-only extension).
				// capImageCacheBreakpoints below clamps to Anthropic's
				// 4-breakpoint limit if a conversation accumulates >4
				// image/file blocks.
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
			})
		case types.ContentTypeFile:
			file := &schemas.ChatInputFile{}
			if cb.Data != "" {
				file.FileData = schemas.Ptr(cb.Data)
			}
			if cb.URL != "" {
				file.FileURL = schemas.Ptr(cb.URL)
			}
			if cb.Filename != "" {
				file.Filename = schemas.Ptr(cb.Filename)
			}
			if cb.MediaType != "" {
				file.FileType = schemas.Ptr(cb.MediaType)
			}
			// Empty file struct (no data, no url) ⇒ drop.
			if file.FileData == nil && file.FileURL == nil {
				continue
			}
			blocks = append(blocks, schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeFile,
				File: file,
				// Same prompt-cache rationale as image blocks; PDFs are
				// even larger (page-count token cost) so caching gives
				// proportionally bigger wins on multi-turn document Q&A.
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
			})
		}
	}

	bfMsg := &schemas.ChatMessage{
		Role: role,
	}

	if len(blocks) == 1 && blocks[0].Type == schemas.ChatContentBlockTypeText {
		bfMsg.Content = &schemas.ChatMessageContent{
			ContentStr: blocks[0].Text,
		}
	} else if len(blocks) > 0 {
		bfMsg.Content = &schemas.ChatMessageContent{
			ContentBlocks: blocks,
		}
	}

	// Attach assistant-only metadata.
	//
	// DeepSeek thinking-mode (verified against deepseek-v4-flash on
	// 2026-05-13) enforces strict schema on assistant messages that
	// carry tool_calls: the `reasoning_content` FIELD must be present
	// — its VALUE may be the empty string. Omitting the field returns
	// 400 "thinking mode must be passed back". Plain-text assistant
	// messages are NOT validated this strictly and may skip the field.
	//
	// So the rule is:
	//   tool_calls → always emit reasoning (echo what we have, or "")
	//   text-only  → only emit reasoning when we actually captured some
	//                AND the caller asked us to forward it
	echoReasoning := includeReasoning && msg.ReasoningContent != ""
	if msg.Role == types.RoleAssistant && (len(toolCalls) > 0 || echoReasoning) {
		am := &schemas.ChatAssistantMessage{}
		if len(toolCalls) > 0 {
			am.ToolCalls = toolCalls
		}
		switch {
		case echoReasoning:
			am.Reasoning = schemas.Ptr(msg.ReasoningContent)
		case len(toolCalls) > 0:
			// Empty placeholder — satisfies DeepSeek's "field must be
			// present" check without spending tokens on reasoning text.
			am.Reasoning = schemas.Ptr("")
		}
		bfMsg.ChatAssistantMessage = am
	}

	return bfMsg
}

// ---------- Tool conversion ----------

// ConvertTools transforms internal ToolSchema to Bifrost ChatTool format.
func ConvertTools(tools []provider.ToolSchema) []schemas.ChatTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]schemas.ChatTool, len(tools))
	for i, t := range tools {
		params := convertInputSchema(t.InputSchema)
		result[i] = schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        t.Name,
				Description: schemas.Ptr(t.Description),
				Parameters:  params,
			},
		}
	}
	return result
}

func convertInputSchema(schema map[string]any) *schemas.ToolFunctionParameters {
	if schema == nil {
		return nil
	}

	params := &schemas.ToolFunctionParameters{}

	if t, ok := schema["type"].(string); ok {
		params.Type = t
	}
	if desc, ok := schema["description"].(string); ok {
		params.Description = &desc
	}
	if req, ok := schema["required"].([]string); ok {
		params.Required = req
	}
	// For properties, we need an OrderedMap — use JSON round-trip.
	if props, ok := schema["properties"]; ok {
		data, err := json.Marshal(props)
		if err == nil {
			var om schemas.OrderedMap
			if json.Unmarshal(data, &om) == nil {
				params.Properties = &om
			}
		}
	}

	return params
}

// buildParams resolves the per-call ChatParameters: request-supplied
// Temperature/MaxTokens win when non-zero, otherwise the Adapter's
// agent-level defaults (already pre-scaled and pre-capped by the
// builder) kick in.
func (a *Adapter) buildParams(req *provider.ChatRequest) *schemas.ChatParameters {
	hasTools := len(req.Tools) > 0

	effectiveTemp := req.Temperature
	if effectiveTemp == 0 && a.defaultTemperature > 0 {
		effectiveTemp = a.defaultTemperature
	}
	effectiveMax := req.MaxTokens
	if effectiveMax == 0 && a.defaultMaxTokens > 0 {
		effectiveMax = a.defaultMaxTokens
	}
	hasTemp := effectiveTemp > 0
	hasMaxTokens := effectiveMax > 0

	if !hasTools && !hasTemp && !hasMaxTokens {
		return nil
	}

	params := &schemas.ChatParameters{}

	if hasTools {
		params.Tools = ConvertTools(req.Tools)
	}
	if hasTemp {
		t := effectiveTemp
		params.Temperature = &t
	}
	if hasMaxTokens {
		m := effectiveMax
		params.MaxCompletionTokens = &m
	}

	return params
}

// ---------- Role & reason mapping ----------

func mapRole(role types.Role) schemas.ChatMessageRole {
	switch role {
	case types.RoleUser:
		return schemas.ChatMessageRoleUser
	case types.RoleAssistant:
		return schemas.ChatMessageRoleAssistant
	case types.RoleSystem:
		return schemas.ChatMessageRoleSystem
	default:
		return schemas.ChatMessageRoleUser
	}
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return reason
	}
}

// ---------- Tool call accumulation ----------

type toolCallAccumulator struct {
	index uint16
	id    string
	name  string
	args  jsonBuilder
}

type jsonBuilder struct {
	buf []byte
}

func (b *jsonBuilder) Write(s string) {
	b.buf = append(b.buf, s...)
}

func (b *jsonBuilder) String() string {
	if len(b.buf) == 0 {
		return "{}"
	}
	return string(b.buf)
}

func accumulateToolCall(acc []toolCallAccumulator, tc schemas.ChatAssistantMessageToolCall) []toolCallAccumulator {
	// Find or create accumulator for this index.
	for i := range acc {
		if acc[i].index == tc.Index {
			// Append arguments delta.
			acc[i].args.Write(tc.Function.Arguments)
			return acc
		}
	}

	// New tool call.
	a := toolCallAccumulator{
		index: tc.Index,
	}
	if tc.ID != nil {
		a.id = *tc.ID
	}
	if tc.Function.Name != nil {
		a.name = *tc.Function.Name
	}
	a.args.Write(tc.Function.Arguments)
	return append(acc, a)
}
