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
	"fmt"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
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

	// Logger for operational events. Nil uses a no-op logger.
	Logger *zap.Logger
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
		client:        client,
		providerKey:   cfg.Provider,
		defaultModel:  cfg.Model,
		fallbackModel: cfg.FallbackModel,
		logger:        logger,
		customHeaders: cfg.CustomHeaders,
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

	bifReq := a.buildChatRequest(model, req)

	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

	stream, bfErr := a.client.ChatCompletionStreamRequest(bfCtx, bifReq)
	if bfErr != nil {
		// On failure, try fallback if available.
		if a.fallbackModel != "" && !a.IsUsingFallback() {
			a.logger.Warn("bifrost: primary model failed, trying fallback",
				zap.String("model", model),
				zap.String("fallback", a.fallbackModel),
				zap.String("error", bfErr.Error.Message),
			)
			a.mu.Lock()
			a.usingFallback = true
			a.mu.Unlock()

			bifReq.Model = a.fallbackModel
			stream, bfErr = a.client.ChatCompletionStreamRequest(bfCtx, bifReq)
			if bfErr != nil {
				return nil, fmt.Errorf("bifrost: fallback also failed: %s", formatBifrostError(bfErr))
			}
		} else {
			return nil, fmt.Errorf("bifrost: stream request failed: %s", formatBifrostError(bfErr))
		}
	}

	eventsCh := make(chan types.StreamEvent, 64)
	var streamErr error

	go func() {
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
	bifReq := &schemas.BifrostChatRequest{
		Provider: a.providerKey,
		Model:    model,
		Input:    convertMessages(req.Messages, req.System),
	}

	params := buildParams(req)
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
func (a *Adapter) consumeStream(stream chan *schemas.BifrostStreamChunk, out chan<- types.StreamEvent) error {
	var toolCalls []toolCallAccumulator

	for chunkPtr := range stream {
		if chunkPtr == nil {
			continue
		}
		chunk := *chunkPtr

		if chunk.BifrostError != nil {
			errMsg := fmt.Sprintf("bifrost stream error: %s", formatBifrostError(chunk.BifrostError))
			out <- types.StreamEvent{
				Type:  types.StreamEventError,
				Error: fmt.Errorf("%s", errMsg),
			}
			return fmt.Errorf("%s", errMsg)
		}

		if chunk.BifrostChatResponse == nil || len(chunk.BifrostChatResponse.Choices) == 0 {
			continue
		}

		choice := chunk.BifrostChatResponse.Choices[0]

		// Streaming delta.
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			delta := choice.ChatStreamResponseChoice.Delta

			// Text content.
			if delta.Content != nil && *delta.Content != "" {
				out <- types.StreamEvent{
					Type: types.StreamEventText,
					Text: *delta.Content,
				}
			}

			// Tool call deltas.
			for _, tc := range delta.ToolCalls {
				toolCalls = accumulateToolCall(toolCalls, tc)
			}
		}

		// Final chunk with finish_reason and usage.
		if choice.FinishReason != nil {
			// Emit accumulated tool calls.
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

			// Build usage from Bifrost response.
			var usage *types.Usage
			if chunk.BifrostChatResponse.Usage != nil {
				u := chunk.BifrostChatResponse.Usage
				usage = &types.Usage{
					InputTokens:  u.PromptTokens,
					OutputTokens: u.CompletionTokens,
				}
				if u.PromptTokensDetails != nil {
					usage.CacheRead = u.PromptTokensDetails.CachedReadTokens
					usage.CacheWrite = u.PromptTokensDetails.CachedWriteTokens
				}
			}

			stopReason := mapFinishReason(*choice.FinishReason)

			out <- types.StreamEvent{
				Type:       types.StreamEventMessageEnd,
				StopReason: stopReason,
				Usage:      usage,
			}
		}
	}
	return nil
}

// ---------- Message conversion ----------

// convertMessages transforms internal messages to Bifrost ChatMessage format.
func convertMessages(msgs []types.Message, systemPrompt string) []schemas.ChatMessage {
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

		bfMsg := convertSingleMessage(msg)
		if bfMsg != nil {
			result = append(result, *bfMsg)
		}
	}
	return result
}

func convertSingleMessage(msg types.Message) *schemas.ChatMessage {
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

	if len(toolCalls) > 0 {
		bfMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
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

func buildParams(req *provider.ChatRequest) *schemas.ChatParameters {
	hasTools := len(req.Tools) > 0
	hasTemp := req.Temperature > 0
	hasMaxTokens := req.MaxTokens > 0

	if !hasTools && !hasTemp && !hasMaxTokens {
		return nil
	}

	params := &schemas.ChatParameters{}

	if hasTools {
		params.Tools = ConvertTools(req.Tools)
	}
	if hasTemp {
		params.Temperature = &req.Temperature
	}
	if hasMaxTokens {
		params.MaxCompletionTokens = &req.MaxTokens
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
