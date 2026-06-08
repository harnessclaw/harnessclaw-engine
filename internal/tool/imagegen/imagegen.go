package imagegen

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	modelregistry "harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const (
	ToolName         = "image_generate"
	generatedDirName = "generated"
	defaultSize      = "1024x1024"
	defaultCount     = 1
	maxCount         = 4
	requestTimeout   = 90 * time.Second
)

var allowedSizes = map[string]bool{
	"1024x1024": true,
	"1024x1536": true,
	"1536x1024": true,
	"512x512":   true,
}

// ConfigSource is satisfied by provider/manager.Manager. It is kept narrow so
// image generation can reuse live provider credentials without entering the
// chat failover path.
type ConfigSource interface {
	CurrentConfig() config.LLMConfig
}

type AgentConfigSource interface {
	CurrentAgent() config.AgentConfig
}

type Tool struct {
	tool.BaseTool
	source   ConfigSource
	registry *modelregistry.Registry
	rootDir  string
	client   *http.Client
	logger   *zap.Logger
}

type Option func(*Tool)

func WithHTTPClient(client *http.Client) Option {
	return func(t *Tool) {
		if client != nil {
			t.client = client
		}
	}
}

func New(source ConfigSource, registry *modelregistry.Registry, rootDir string, logger *zap.Logger, opts ...Option) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	t := &Tool{
		source:   source,
		registry: registry,
		rootDir:  rootDir,
		client:   &http.Client{Timeout: requestTimeout},
		logger:   logger.Named("imagegen"),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (*Tool) Name() string { return ToolName }
func (*Tool) Description() string {
	return "Generate images from a text prompt using configured image-generation models. Returns local file paths for generated images."
}
func (*Tool) IsReadOnly() bool              { return false }
func (*Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*Tool) IsConcurrencySafe() bool       { return true }
func (t *Tool) IsEnabled() bool             { return t.source != nil && t.registry != nil }

func (*Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Image prompt to generate.",
				"minLength":   1,
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional model selector. Accepts provider:endpoint, provider/model_id, or model_id. When agent.image_generation is configured, the selector must match it.",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Image size.",
				"enum":        []string{"1024x1024", "1024x1536", "1536x1024", "512x512"},
				"default":     defaultSize,
			},
			"n": map[string]any{
				"type":        "integer",
				"description": "Number of images to generate.",
				"minimum":     1,
				"maximum":     maxCount,
				"default":     defaultCount,
			},
			"quality": map[string]any{
				"type":        "string",
				"description": "Optional provider quality hint.",
			},
			"style": map[string]any{
				"type":        "string",
				"description": "Optional provider style hint.",
			},
		},
		"required": []string{"prompt"},
	}
}

type input struct {
	Prompt  string `json:"prompt"`
	Model   string `json:"model"`
	Size    string `json:"size"`
	N       int    `json:"n"`
	Quality string `json:"quality"`
	Style   string `json:"style"`
}

func (t *Tool) ValidateInput(raw json.RawMessage) error {
	in, err := parseInput(raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if in.N < 1 || in.N > maxCount {
		return fmt.Errorf("n must be between 1 and %d", maxCount)
	}
	if !allowedSizes[in.Size] {
		return fmt.Errorf("size %q is not supported", in.Size)
	}
	return nil
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	in, err := parseInput(raw)
	if err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if err := t.ValidateInput(raw); err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if t.source == nil || t.registry == nil {
		return errResult("image_generate is not configured", types.ToolErrorInternal), nil
	}

	target, err := t.resolveTarget(in.Model)
	if err != nil {
		return errResult(err.Error(), types.ToolErrorInvalidInput), nil
	}
	sessionRoot, err := t.resolveSessionRoot(ctx)
	if err != nil {
		return errResult(err.Error(), types.ToolErrorInternal), nil
	}
	outDir := filepath.Join(sessionRoot, generatedDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return errResult("create generated directory: "+err.Error(), types.ToolErrorInternal), nil
	}

	resp, err := t.callProvider(ctx, target, in)
	if err != nil {
		return errResult("image generation request failed: "+err.Error(), types.ToolErrorDependencyFail), nil
	}

	images := make([]GeneratedImage, 0, len(resp.Data))
	for idx, item := range resp.Data {
		body, mimeType, err := t.resolveImageBytes(ctx, item)
		if err != nil {
			return errResult(fmt.Sprintf("decode image %d: %v", idx, err), types.ToolErrorDependencyFail), nil
		}
		ext := extensionForMIME(mimeType)
		name := fmt.Sprintf("%s-%02d%s", time.Now().UTC().Format("20060102T150405"), idx+1, ext)
		if suffix := randomSuffix(); suffix != "" {
			name = fmt.Sprintf("%s-%s-%02d%s", time.Now().UTC().Format("20060102T150405"), suffix, idx+1, ext)
		}
		p := filepath.Join(outDir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			return errResult("write generated image: "+err.Error(), types.ToolErrorInternal), nil
		}
		prompt := strings.TrimSpace(item.RevisedPrompt)
		if prompt == "" {
			prompt = in.Prompt
		}
		images = append(images, GeneratedImage{
			Path:   p,
			MIME:   mimeType,
			Bytes:  len(body),
			Model:  target.ModelID,
			Prompt: prompt,
			Size:   in.Size,
		})
	}
	if len(images) == 0 {
		return errResult("image generation response did not include any images", types.ToolErrorModelError), nil
	}
	t.emitDeliverables(ctx, images)

	return &types.ToolResult{
		Content: fmt.Sprintf("generated %d image(s) with %s; files are available in %s", len(images), target.ModelID, outDir),
		Metadata: map[string]any{
			"images":   images,
			"model":    target.ModelID,
			"provider": target.ProviderName,
			"endpoint": target.EndpointName,
			"prompt":   in.Prompt,
		},
	}, nil
}

func parseInput(raw json.RawMessage) (input, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, err
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	in.Model = strings.TrimSpace(in.Model)
	in.Size = strings.TrimSpace(in.Size)
	if in.Size == "" {
		in.Size = defaultSize
	}
	if in.N == 0 {
		in.N = defaultCount
	}
	in.Quality = strings.TrimSpace(in.Quality)
	in.Style = strings.TrimSpace(in.Style)
	return in, nil
}

type targetEndpoint struct {
	ProviderName string
	EndpointName string
	ModelID      string
	BaseURL      string
	Path         string
	APIKey       string
	AuthHeader   string
	AuthPrefix   string
}

func (t *Tool) resolveTarget(selector string) (targetEndpoint, error) {
	cfg := t.source.CurrentConfig()
	configured := ""
	if agentSource, ok := t.source.(AgentConfigSource); ok {
		configured = strings.TrimSpace(agentSource.CurrentAgent().ImageGeneration)
		if configured == "" {
			return targetEndpoint{}, errors.New("agent.image_generation is not configured; please enable an image-generation model in Settings > Models, then select it in Settings > Agent")
		}
	}

	candidates := t.imageEndpoints(cfg)
	if len(candidates) == 0 {
		if configured != "" {
			return targetEndpoint{}, fmt.Errorf("configured agent.image_generation %q is not available; please enable its provider and model in Settings > Models", configured)
		}
		return targetEndpoint{}, errors.New("no image_generation endpoint is configured")
	}
	if configured != "" {
		for _, c := range candidates {
			if !c.matches(configured) {
				continue
			}
			if selector != "" && !c.matches(selector) {
				return targetEndpoint{}, fmt.Errorf("model %q does not match configured agent.image_generation %q", selector, configured)
			}
			return c, nil
		}
		return targetEndpoint{}, fmt.Errorf("configured agent.image_generation %q is not a configured image_generation endpoint", configured)
	}
	if selector == "" {
		return targetEndpoint{}, errors.New("agent.image_generation is not configured; please select an image-generation model in Settings > Agent")
	}
	for _, c := range candidates {
		if c.matches(selector) {
			return c, nil
		}
	}
	return targetEndpoint{}, fmt.Errorf("model %q is not a configured image_generation endpoint", selector)
}

func (c targetEndpoint) matches(selector string) bool {
	return selector == config.FormatChainEntry(c.ProviderName, c.EndpointName) ||
		selector == c.ProviderName+"/"+c.ModelID ||
		selector == c.ModelID ||
		selector == c.EndpointName
}

func (t *Tool) imageEndpoints(cfg config.LLMConfig) []targetEndpoint {
	provNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		provNames = append(provNames, name)
	}
	sort.Strings(provNames)
	var out []targetEndpoint
	for _, provName := range provNames {
		provCfg := cfg.Providers[provName]
		if provCfg.Disabled || provCfg.APIKey == "" {
			continue
		}
		provSpec := t.lookupProviderSpec(provName, provCfg)
		if provSpec == nil || provSpec.Endpoints.ImagesGenerations == nil || strings.TrimSpace(*provSpec.Endpoints.ImagesGenerations) == "" {
			continue
		}
		baseURL := strings.TrimSpace(provCfg.BaseURL)
		if baseURL == "" {
			baseURL = provSpec.BaseURL
		}
		if baseURL == "" {
			continue
		}
		epNames := make([]string, 0, len(provCfg.Endpoints))
		for epName := range provCfg.Endpoints {
			epNames = append(epNames, epName)
		}
		sort.Strings(epNames)
		for _, epName := range epNames {
			epCfg := provCfg.Endpoints[epName]
			if epCfg.Disabled {
				continue
			}
			if strings.TrimSpace(epCfg.Model) == "" {
				continue
			}
			if !t.endpointSupportsImageGeneration(provName, provCfg, epCfg) {
				continue
			}
			authHeader := provSpec.Auth.KeyHeader
			if authHeader == "" {
				authHeader = "Authorization"
			}
			authPrefix := provSpec.Auth.KeyPrefix
			if authPrefix == "" && strings.EqualFold(provSpec.Auth.Type, "bearer") {
				authPrefix = "Bearer "
			}
			out = append(out, targetEndpoint{
				ProviderName: provName,
				EndpointName: epName,
				ModelID:      strings.TrimSpace(epCfg.Model),
				BaseURL:      strings.TrimRight(baseURL, "/"),
				Path:         strings.TrimSpace(*provSpec.Endpoints.ImagesGenerations),
				APIKey:       provCfg.APIKey,
				AuthHeader:   authHeader,
				AuthPrefix:   authPrefix,
			})
		}
	}
	return out
}

func (t *Tool) lookupProviderSpec(provName string, provCfg config.ProviderConfig) *modelregistry.ProviderSpec {
	if spec := t.registry.LookupProvider(provName); spec != nil {
		return spec
	}
	return t.registry.LookupProvider(provCfg.Type)
}

func (t *Tool) endpointSupportsImageGeneration(provName string, provCfg config.ProviderConfig, epCfg config.EndpointConfig) bool {
	if len(epCfg.ModelType) > 0 {
		return modelregistry.SupportsFromTokens(epCfg.ModelType).ImageGeneration
	}
	if spec := t.registry.LookupByProviderAndModelID(provName, epCfg.Model); spec != nil {
		return spec.Supports.ImageGeneration
	}
	if spec := t.registry.LookupByProviderAndModelID(provCfg.Type, epCfg.Model); spec != nil {
		return spec.Supports.ImageGeneration
	}
	return false
}

type providerResponse struct {
	Data         []providerImage `json:"data"`
	Size         string          `json:"size"`
	Quality      string          `json:"quality"`
	OutputFormat string          `json:"output_format"`
}

type providerImage struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
	MIME          string `json:"mime_type"`
}

func (t *Tool) callProvider(ctx context.Context, target targetEndpoint, in input) (providerResponse, error) {
	body := map[string]any{
		"model":           target.ModelID,
		"prompt":          in.Prompt,
		"n":               in.N,
		"size":            in.Size,
		"response_format": "b64_json",
	}
	if in.Quality != "" {
		body["quality"] = in.Quality
	}
	if in.Style != "" {
		body["style"] = in.Style
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return providerResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(target.BaseURL, target.Path), bytes.NewReader(payload))
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if target.APIKey != "" && target.AuthHeader != "" {
		req.Header.Set(target.AuthHeader, target.AuthPrefix+target.APIKey)
	}
	res, err := t.client.Do(req)
	if err != nil {
		return providerResponse{}, err
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024))
	if err != nil {
		return providerResponse{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return providerResponse{}, fmt.Errorf("HTTP %d: %s", res.StatusCode, summarizeBody(respBody))
	}
	var parsed providerResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return providerResponse{}, err
	}
	return parsed, nil
}

func (t *Tool) resolveImageBytes(ctx context.Context, item providerImage) ([]byte, string, error) {
	mimeType := strings.TrimSpace(item.MIME)
	if item.B64JSON != "" {
		data, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return nil, "", err
		}
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		return data, normalizeImageMIME(mimeType), nil
	}
	if item.URL == "" {
		return nil, "", errors.New("missing b64_json/url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 25*1024*1024))
	if err != nil {
		return nil, "", err
	}
	if mimeType == "" {
		mimeType = res.Header.Get("Content-Type")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, normalizeImageMIME(mimeType), nil
}

type GeneratedImage struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Bytes  int    `json:"bytes"`
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
}

func (t *Tool) emitDeliverables(ctx context.Context, images []GeneratedImage) {
	out, ok := tool.GetEventOut(ctx)
	if !ok || out == nil {
		return
	}
	for _, img := range images {
		select {
		case out <- types.EngineEvent{
			Type: types.EngineEventDeliverable,
			Deliverable: &types.Deliverable{
				FilePath: img.Path,
				ByteSize: img.Bytes,
			},
		}:
		default:
		}
	}
}

func (t *Tool) resolveSessionRoot(ctx context.Context) (string, error) {
	scope, ok := tool.AgentScopeFromCtx(ctx)
	if ok && strings.TrimSpace(scope.SessionRoot) != "" {
		return scope.SessionRoot, nil
	}
	producer, ok := tool.GetArtifactProducer(ctx)
	if ok && strings.TrimSpace(producer.SessionID) != "" && strings.TrimSpace(t.rootDir) != "" {
		return workspace.SessionRoot(t.rootDir, producer.SessionID), nil
	}
	return "", errors.New("SessionRoot missing in ctx — engine configuration error")
}

func joinURL(base, endpointPath string) string {
	u, err := url.Parse(base)
	if err != nil {
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpointPath, "/")
	}
	u.Path = path.Join(u.Path, endpointPath)
	return u.String()
}

func normalizeImageMIME(value string) string {
	mt, _, err := mime.ParseMediaType(value)
	if err == nil {
		value = mt
	}
	switch strings.ToLower(value) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/webp":
		return "image/webp"
	case "image/gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func extensionForMIME(mimeType string) string {
	switch normalizeImageMIME(mimeType) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func randomSuffix() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func summarizeBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}

func errResult(msg string, errType types.ToolErrorType) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: errType}
}
