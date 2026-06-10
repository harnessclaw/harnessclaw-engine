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
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	modelregistry "harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const (
	ToolName         = "image_generate"
	generatedDirName = "generated"
	attachmentsDir   = "user-attachments"
	defaultSize      = "1024x1024"
	defaultCount     = 1
	maxCount         = 4
	maxSourceImages  = 14
	openAISourceMax  = 50 * 1024 * 1024
	doubaoSourceMax  = 30 * 1024 * 1024
	requestTimeout   = 5 * time.Minute
	tlsHandshakeWait = time.Minute
)

const (
	modeAuto     = "auto"
	modeGenerate = "generate"
	modeEdit     = "edit"
)

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
		client:   newHTTPClient(),
		logger:   logger.Named("imagegen"),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = tlsHandshakeWait
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
	}
}

func (*Tool) Name() string { return ToolName }
func (*Tool) Description() string {
	return "Generate or edit images using configured image-generation models. Use current attached images or source_images when the user asks to transform, edit, or reference an existing image. Returns local file paths for generated images."
}
func (*Tool) IsReadOnly() bool              { return false }
func (*Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*Tool) IsConcurrencySafe() bool       { return true }
func (*Tool) IsLongRunning() bool           { return true }
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
				"description": "Image size. Supports provider-specific values such as 1024x1024, 1536x1024, 1024x1536, auto, or Doubao values like 2K/3K/4K.",
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
			"mode": map[string]any{
				"type":        "string",
				"description": "Generation mode. Use auto unless the user explicitly asks to ignore attached images.",
				"enum":        []string{modeAuto, modeGenerate, modeEdit},
				"default":     modeAuto,
			},
			"source_images": map[string]any{
				"type":        "array",
				"description": "Optional reference/source images for image-to-image. Each item must provide path or url. Inline base64 is intentionally not accepted in tool input.",
				"maxItems":    maxSourceImages,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Server-local image path."},
						"url":  map[string]any{"type": "string", "description": "Remote image URL."},
					},
					"additionalProperties": false,
				},
			},
			"use_attached_images": map[string]any{
				"type":        "boolean",
				"description": "When true, use image attachments from the current user turn if source_images is empty.",
				"default":     true,
			},
			"mask": map[string]any{
				"type":        "object",
				"description": "Optional mask image for OpenAI-compatible edits. Omit unless the user explicitly provided a real mask image file. Never pass empty strings or placeholder paths.",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Server-local mask image path."},
					"url":  map[string]any{"type": "string", "description": "Remote mask image URL."},
				},
				"additionalProperties": false,
			},
			"output_format": map[string]any{
				"type":        "string",
				"description": "Optional output image format.",
				"enum":        []string{"png", "jpeg", "webp"},
			},
			"output_compression": map[string]any{
				"type":        "integer",
				"description": "Optional compression level for providers that support jpeg/webp compression.",
				"minimum":     0,
				"maximum":     100,
			},
			"background": map[string]any{
				"type":        "string",
				"description": "Optional provider background hint.",
			},
			"watermark": map[string]any{
				"type":        "boolean",
				"description": "Optional Doubao watermark flag. Only sent when explicitly provided.",
			},
		},
		"required": []string{"prompt"},
	}
}

type input struct {
	Prompt            string             `json:"prompt"`
	Model             string             `json:"model"`
	Size              string             `json:"size"`
	N                 int                `json:"n"`
	Quality           string             `json:"quality"`
	Style             string             `json:"style"`
	Mode              string             `json:"mode"`
	SourceImages      []imageSourceInput `json:"source_images"`
	UseAttachedImages *bool              `json:"use_attached_images"`
	Mask              *imageSourceInput  `json:"mask"`
	OutputFormat      string             `json:"output_format"`
	OutputCompression *int               `json:"output_compression"`
	Background        string             `json:"background"`
	Watermark         *bool              `json:"watermark"`
}

type imageSourceInput struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	Data string `json:"data"`
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
	if !validSizeValue(in.Size) {
		return fmt.Errorf("size %q is not supported", in.Size)
	}
	if !validMode(in.Mode) {
		return fmt.Errorf("mode %q is not supported", in.Mode)
	}
	if len(in.SourceImages) > maxSourceImages {
		return fmt.Errorf("source_images supports at most %d images", maxSourceImages)
	}
	for idx, source := range in.SourceImages {
		if err := validateImageSourceInput(source); err != nil {
			return fmt.Errorf("source_images[%d]: %w", idx, err)
		}
	}
	if in.Mask != nil {
		if err := validateImageSourceInput(*in.Mask); err != nil {
			return fmt.Errorf("mask: %w", err)
		}
	}
	if in.OutputFormat != "" && in.OutputFormat != "png" && in.OutputFormat != "jpeg" && in.OutputFormat != "webp" {
		return fmt.Errorf("output_format %q is not supported", in.OutputFormat)
	}
	if in.OutputCompression != nil && (*in.OutputCompression < 0 || *in.OutputCompression > 100) {
		return errors.New("output_compression must be between 0 and 100")
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
	sources, err := t.collectSourceImages(ctx, in, sessionRoot)
	if err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	mode, err := effectiveMode(in, sources)
	if err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if err := validateModeForTarget(target, mode, sources, in); err != nil {
		return errResult(err.Error(), types.ToolErrorInvalidInput), nil
	}
	outDir := filepath.Join(sessionRoot, generatedDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return errResult("create generated directory: "+err.Error(), types.ToolErrorInternal), nil
	}

	resp, err := t.callProvider(ctx, target, in, mode, sources)
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
		itemSize := strings.TrimSpace(item.Size)
		if itemSize == "" {
			itemSize = in.Size
		}
		images = append(images, GeneratedImage{
			Path:   p,
			MIME:   mimeType,
			Bytes:  len(body),
			Model:  target.ModelID,
			Prompt: prompt,
			Size:   itemSize,
		})
	}
	if len(images) == 0 {
		return errResult("image generation response did not include any images", types.ToolErrorModelError), nil
	}
	t.emitDeliverables(ctx, images)

	return &types.ToolResult{
		Content: resultContent(images, target.ModelID, mode, len(sources), outDir),
		Metadata: map[string]any{
			"images":              images,
			"model":               target.ModelID,
			"provider":            target.ProviderName,
			"endpoint":            target.EndpointName,
			"prompt":              in.Prompt,
			"mode":                mode,
			"source_images_count": len(sources),
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
	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.Mode == "" {
		in.Mode = modeAuto
	}
	for i := range in.SourceImages {
		in.SourceImages[i].Path = strings.TrimSpace(in.SourceImages[i].Path)
		in.SourceImages[i].URL = strings.TrimSpace(in.SourceImages[i].URL)
		in.SourceImages[i].Data = strings.TrimSpace(in.SourceImages[i].Data)
	}
	in.SourceImages = compactSourceImages(in.SourceImages)
	if in.Mask != nil {
		in.Mask.Path = strings.TrimSpace(in.Mask.Path)
		in.Mask.URL = strings.TrimSpace(in.Mask.URL)
		in.Mask.Data = strings.TrimSpace(in.Mask.Data)
		if isEmptyImageSourceInput(*in.Mask) {
			in.Mask = nil
		}
	}
	in.OutputFormat = strings.ToLower(strings.TrimSpace(in.OutputFormat))
	in.Background = strings.TrimSpace(in.Background)
	return in, nil
}

func compactSourceImages(sources []imageSourceInput) []imageSourceInput {
	if len(sources) == 0 {
		return sources
	}
	out := sources[:0]
	for _, source := range sources {
		if isEmptyImageSourceInput(source) {
			continue
		}
		out = append(out, source)
	}
	return out
}

func isEmptyImageSourceInput(source imageSourceInput) bool {
	return source.Path == "" && source.URL == "" && source.Data == ""
}

type targetEndpoint struct {
	ProviderName       string
	ProviderType       string
	EndpointName       string
	ModelID            string
	BaseURL            string
	GenerationPath     string
	ImageEditPath      string
	APIKey             string
	AuthHeader         string
	AuthPrefix         string
	SupportsImageInput bool
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
		provType := strings.TrimSpace(provCfg.Type)
		if provType == "" {
			provType = provName
		}
		provSpec := t.lookupProviderSpec(provName, provCfg)
		if provSpec == nil || provSpec.Endpoints.ImagesGenerations == nil || strings.TrimSpace(*provSpec.Endpoints.ImagesGenerations) == "" {
			continue
		}
		imageEditPath := ""
		if provSpec.Endpoints.ImageEdits != nil {
			imageEditPath = strings.TrimSpace(*provSpec.Endpoints.ImageEdits)
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
				ProviderName:       provName,
				ProviderType:       provType,
				EndpointName:       epName,
				ModelID:            strings.TrimSpace(epCfg.Model),
				BaseURL:            strings.TrimRight(baseURL, "/"),
				GenerationPath:     strings.TrimSpace(*provSpec.Endpoints.ImagesGenerations),
				ImageEditPath:      imageEditPath,
				APIKey:             provCfg.APIKey,
				AuthHeader:         authHeader,
				AuthPrefix:         authPrefix,
				SupportsImageInput: t.endpointSupportsImageInput(provName, provCfg, epCfg),
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

func (t *Tool) endpointSupportsImageInput(provName string, provCfg config.ProviderConfig, epCfg config.EndpointConfig) bool {
	if len(epCfg.ModelType) > 0 {
		return modelregistry.SupportsFromTokens(epCfg.ModelType).Vision
	}
	if spec := t.registry.LookupByProviderAndModelID(provName, epCfg.Model); spec != nil {
		return spec.Supports.Vision
	}
	if spec := t.registry.LookupByProviderAndModelID(provCfg.Type, epCfg.Model); spec != nil {
		return spec.Supports.Vision
	}
	return false
}

func validMode(mode string) bool {
	return mode == modeAuto || mode == modeGenerate || mode == modeEdit
}

func validSizeValue(size string) bool {
	if size == "" || size == "auto" || size == "2K" || size == "3K" || size == "4K" {
		return true
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return false
	}
	width, err := strconv.Atoi(parts[0])
	if err != nil || width <= 0 {
		return false
	}
	height, err := strconv.Atoi(parts[1])
	return err == nil && height > 0
}

func validateImageSourceInput(source imageSourceInput) error {
	if source.Data != "" {
		return errors.New("inline image data is not accepted; use path or url")
	}
	hasPath := source.Path != ""
	hasURL := source.URL != ""
	if hasPath == hasURL {
		return errors.New("exactly one of path or url is required")
	}
	if hasURL {
		u, err := url.Parse(source.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid url %q", source.URL)
		}
	}
	return nil
}

func (t *Tool) collectSourceImages(ctx context.Context, in input, sessionRoot string) ([]sourceImage, error) {
	if in.Mode == modeGenerate && (len(in.SourceImages) > 0 || in.Mask != nil) {
		return nil, errors.New("mode generate cannot include source_images or mask")
	}
	sources := make([]sourceImage, 0, len(in.SourceImages))
	for _, source := range in.SourceImages {
		sources = append(sources, sourceImage{
			Path: source.Path,
			URL:  source.URL,
		})
	}
	if len(sources) > 0 || !shouldUseAttachedImages(in) {
		return sources, nil
	}
	if currentImages, ok := tool.CurrentImagesFromCtx(ctx); ok {
		for _, image := range currentImages {
			if image.Path == "" && image.URL == "" && image.Data == "" {
				continue
			}
			sources = append(sources, sourceImage{
				Path:      image.Path,
				URL:       image.URL,
				Data:      image.Data,
				MediaType: image.MediaType,
				Filename:  image.Filename,
			})
		}
	}
	if len(sources) > maxSourceImages {
		return nil, fmt.Errorf("current image attachments exceed max source image count %d", maxSourceImages)
	}
	if len(sources) == 0 && in.Mode == modeEdit {
		if fallback, ok := latestSessionImageSource(sessionRoot); ok {
			sources = append(sources, fallback)
		}
	}
	return sources, nil
}

func shouldUseAttachedImages(in input) bool {
	if in.Mode == modeGenerate {
		return false
	}
	if in.UseAttachedImages != nil {
		return *in.UseAttachedImages
	}
	return true
}

func effectiveMode(in input, sources []sourceImage) (string, error) {
	if in.Mode == modeGenerate {
		return modeGenerate, nil
	}
	if in.Mask != nil && len(sources) == 0 {
		return "", errors.New("mask requires source_images or current attached images")
	}
	if in.Mode == modeEdit {
		if len(sources) == 0 {
			return "", errors.New("mode edit requires source_images or current attached images")
		}
		return modeEdit, nil
	}
	if len(sources) > 0 || in.Mask != nil {
		return modeEdit, nil
	}
	return modeGenerate, nil
}

func validateModeForTarget(target targetEndpoint, mode string, sources []sourceImage, in input) error {
	if mode != modeEdit {
		return nil
	}
	if len(sources) == 0 {
		return errors.New("image edit mode requires at least one source image")
	}
	if !target.SupportsImageInput {
		return fmt.Errorf("configured image-generation model %q does not support image input", target.ModelID)
	}
	if target.isDoubao() {
		if in.Mask != nil {
			return errors.New("mask is not supported for Doubao Seedream image generation")
		}
		return nil
	}
	if target.ImageEditPath == "" {
		return fmt.Errorf("provider %q does not define image_edits endpoint for image input", target.ProviderName)
	}
	return nil
}

func (c targetEndpoint) isDoubao() bool {
	return strings.EqualFold(c.ProviderType, "doubao") ||
		strings.EqualFold(c.ProviderName, "doubao") ||
		strings.Contains(strings.ToLower(c.ModelID), "seedream")
}

func (c targetEndpoint) isGPTImage2() bool {
	return strings.EqualFold(c.ModelID, "gpt-image-2")
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
	Size          string `json:"size"`
}

type sourceImage struct {
	Path      string
	URL       string
	Data      string
	MediaType string
	Filename  string
}

type resolvedSourceImage struct {
	Data      []byte
	MediaType string
	Filename  string
	URL       string
}

func (t *Tool) callProvider(ctx context.Context, target targetEndpoint, in input, mode string, sources []sourceImage) (providerResponse, error) {
	if target.isDoubao() {
		return t.callDoubaoGeneration(ctx, target, in, sources)
	}
	if mode == modeEdit {
		return t.callOpenAIEdit(ctx, target, in, sources)
	}
	return t.callOpenAIGeneration(ctx, target, in)
}

func (t *Tool) callOpenAIGeneration(ctx context.Context, target targetEndpoint, in input) (providerResponse, error) {
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
	if in.Style != "" && !target.isGPTImage2() {
		body["style"] = in.Style
	}
	addCommonOpenAIOutputOptions(body, in)
	payload, err := json.Marshal(body)
	if err != nil {
		return providerResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(target.BaseURL, target.GenerationPath), bytes.NewReader(payload))
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.doProviderRequest(req, target)
}

func (t *Tool) callOpenAIEdit(ctx context.Context, target targetEndpoint, in input, sources []sourceImage) (providerResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model":           target.ModelID,
		"prompt":          in.Prompt,
		"n":               strconv.Itoa(in.N),
		"size":            in.Size,
		"response_format": "b64_json",
	} {
		if err := writer.WriteField(key, value); err != nil {
			return providerResponse{}, err
		}
	}
	if in.Quality != "" {
		if err := writer.WriteField("quality", in.Quality); err != nil {
			return providerResponse{}, err
		}
	}
	if err := writeOpenAIOutputFields(writer, in); err != nil {
		return providerResponse{}, err
	}
	for idx, source := range sources {
		resolved, err := t.resolveSourceImage(ctx, target, source)
		if err != nil {
			return providerResponse{}, fmt.Errorf("source image %d: %w", idx, err)
		}
		file, err := createImageFormFile(writer, "image", resolved)
		if err != nil {
			return providerResponse{}, err
		}
		if _, err := file.Write(resolved.Data); err != nil {
			return providerResponse{}, err
		}
	}
	if in.Mask != nil {
		resolved, err := t.resolveSourceImage(ctx, target, sourceImage{Path: in.Mask.Path, URL: in.Mask.URL})
		if err != nil {
			return providerResponse{}, fmt.Errorf("mask: %w", err)
		}
		file, err := createImageFormFile(writer, "mask", resolved)
		if err != nil {
			return providerResponse{}, err
		}
		if _, err := file.Write(resolved.Data); err != nil {
			return providerResponse{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return providerResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(target.BaseURL, target.ImageEditPath), &body)
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return t.doProviderRequest(req, target)
}

func createImageFormFile(writer *multipart.Writer, fieldName string, image resolvedSourceImage) (io.Writer, error) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     fieldName,
		"filename": image.Filename,
	}))
	header.Set("Content-Type", image.MediaType)
	return writer.CreatePart(header)
}

func (t *Tool) callDoubaoGeneration(ctx context.Context, target targetEndpoint, in input, sources []sourceImage) (providerResponse, error) {
	body := map[string]any{
		"model":           target.ModelID,
		"prompt":          in.Prompt,
		"size":            in.Size,
		"response_format": "b64_json",
	}
	if len(sources) > 0 {
		refs := make([]string, 0, len(sources))
		for idx, source := range sources {
			ref, err := t.doubaoImageReference(ctx, target, source)
			if err != nil {
				return providerResponse{}, fmt.Errorf("source image %d: %w", idx, err)
			}
			refs = append(refs, ref)
		}
		if len(refs) == 1 {
			body["image"] = refs[0]
		} else {
			body["image"] = refs
		}
	}
	if in.N > 1 {
		body["sequential_image_generation"] = "auto"
		body["sequential_image_generation_options"] = map[string]int{"max_images": in.N}
	} else {
		body["sequential_image_generation"] = "disabled"
	}
	if in.OutputFormat != "" {
		body["output_format"] = in.OutputFormat
	}
	if in.Watermark != nil {
		body["watermark"] = *in.Watermark
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return providerResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(target.BaseURL, target.GenerationPath), bytes.NewReader(payload))
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.doProviderRequest(req, target)
}

func (t *Tool) doProviderRequest(req *http.Request, target targetEndpoint) (providerResponse, error) {
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

func addCommonOpenAIOutputOptions(body map[string]any, in input) {
	if in.OutputFormat != "" {
		body["output_format"] = in.OutputFormat
	}
	if shouldSendOutputCompression(in) {
		body["output_compression"] = *in.OutputCompression
	}
	if in.Background != "" {
		body["background"] = in.Background
	}
}

func writeOpenAIOutputFields(writer *multipart.Writer, in input) error {
	fields := map[string]string{}
	if in.OutputFormat != "" {
		fields["output_format"] = in.OutputFormat
	}
	if shouldSendOutputCompression(in) {
		fields["output_compression"] = strconv.Itoa(*in.OutputCompression)
	}
	if in.Background != "" {
		fields["background"] = in.Background
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	return nil
}

func shouldSendOutputCompression(in input) bool {
	return in.OutputCompression != nil && (in.OutputFormat == "jpeg" || in.OutputFormat == "webp")
}

func (t *Tool) doubaoImageReference(ctx context.Context, target targetEndpoint, source sourceImage) (string, error) {
	if source.URL != "" && source.Data == "" && source.Path == "" {
		return source.URL, nil
	}
	resolved, err := t.resolveSourceImage(ctx, target, source)
	if err != nil {
		return "", err
	}
	return "data:" + resolved.MediaType + ";base64," + base64.StdEncoding.EncodeToString(resolved.Data), nil
}

func (t *Tool) resolveSourceImage(ctx context.Context, target targetEndpoint, source sourceImage) (resolvedSourceImage, error) {
	if source.URL != "" {
		return t.downloadSourceImage(ctx, target, source.URL)
	}
	if source.Data != "" {
		return decodeInlineSourceImage(target, source)
	}
	if source.Path == "" {
		return resolvedSourceImage{}, errors.New("missing source path/url/data")
	}
	return t.readSourceImagePath(ctx, target, source.Path)
}

func (t *Tool) readSourceImagePath(ctx context.Context, target targetEndpoint, rawPath string) (resolvedSourceImage, error) {
	p := rawPath
	if !filepath.IsAbs(p) {
		scope, ok := tool.AgentScopeFromCtx(ctx)
		if ok && scope.SessionRoot != "" {
			p = filepath.Join(scope.SessionRoot, p)
		}
	}
	info, err := os.Stat(p)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	maxBytes := maxSourceBytes(target)
	if info.Size() > maxBytes {
		return resolvedSourceImage{}, fmt.Errorf("image exceeds %d bytes", maxBytes)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	mimeType, err := sourceMIME("", p, data)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	if err := validateSourceMIME(target, mimeType); err != nil {
		return resolvedSourceImage{}, err
	}
	return resolvedSourceImage{
		Data:      data,
		MediaType: mimeType,
		Filename:  filepath.Base(p),
	}, nil
}

func (t *Tool) downloadSourceImage(ctx context.Context, target targetEndpoint, rawURL string) (resolvedSourceImage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	res, err := t.client.Do(req)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return resolvedSourceImage{}, fmt.Errorf("download HTTP %d", res.StatusCode)
	}
	maxBytes := maxSourceBytes(target)
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return resolvedSourceImage{}, err
	}
	if int64(len(data)) > maxBytes {
		return resolvedSourceImage{}, fmt.Errorf("image exceeds %d bytes", maxBytes)
	}
	u, _ := url.Parse(rawURL)
	filename := "source"
	if u != nil && path.Base(u.Path) != "." && path.Base(u.Path) != "/" {
		filename = path.Base(u.Path)
	}
	mimeType, err := sourceMIME(res.Header.Get("Content-Type"), filename, data)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	if err := validateSourceMIME(target, mimeType); err != nil {
		return resolvedSourceImage{}, err
	}
	return resolvedSourceImage{
		Data:      data,
		MediaType: mimeType,
		Filename:  ensureImageFilename(filename, mimeType),
	}, nil
}

func decodeInlineSourceImage(target targetEndpoint, source sourceImage) (resolvedSourceImage, error) {
	mimeType := source.MediaType
	payload := source.Data
	if strings.HasPrefix(payload, "data:") {
		mt, b64, ok := splitDataURL(payload)
		if !ok {
			return resolvedSourceImage{}, errors.New("invalid data URL")
		}
		mimeType = mt
		payload = b64
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	maxBytes := maxSourceBytes(target)
	if int64(len(data)) > maxBytes {
		return resolvedSourceImage{}, fmt.Errorf("image exceeds %d bytes", maxBytes)
	}
	filename := source.Filename
	if filename == "" {
		filename = "attached"
	}
	mimeType, err = sourceMIME(mimeType, filename, data)
	if err != nil {
		return resolvedSourceImage{}, err
	}
	if err := validateSourceMIME(target, mimeType); err != nil {
		return resolvedSourceImage{}, err
	}
	return resolvedSourceImage{
		Data:      data,
		MediaType: mimeType,
		Filename:  ensureImageFilename(filename, mimeType),
	}, nil
}

func maxSourceBytes(target targetEndpoint) int64 {
	if target.isDoubao() {
		return doubaoSourceMax
	}
	return openAISourceMax
}

func splitDataURL(value string) (string, string, bool) {
	prefix, payload, ok := strings.Cut(value, ",")
	if !ok {
		return "", "", false
	}
	if !strings.HasPrefix(prefix, "data:") || !strings.Contains(prefix, ";base64") {
		return "", "", false
	}
	mimeType := strings.TrimPrefix(strings.Split(prefix, ";")[0], "data:")
	return mimeType, payload, true
}

func sourceMIME(explicit, filename string, data []byte) (string, error) {
	if explicit != "" {
		if mt := normalizeSourceMIME(explicit); mt != "" {
			return mt, nil
		}
	}
	if ext := filepath.Ext(filename); ext != "" {
		if mt := normalizeSourceMIME(mime.TypeByExtension(ext)); mt != "" {
			return mt, nil
		}
	}
	if len(data) > 0 {
		if mt := normalizeSourceMIME(http.DetectContentType(data)); mt != "" {
			return mt, nil
		}
	}
	return "", errors.New("unsupported image mime type")
}

func normalizeSourceMIME(value string) string {
	mt, _, err := mime.ParseMediaType(value)
	if err == nil {
		value = mt
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	case "image/gif":
		return "image/gif"
	case "image/bmp":
		return "image/bmp"
	case "image/tiff", "image/tif":
		return "image/tiff"
	case "image/heic":
		return "image/heic"
	case "image/heif":
		return "image/heif"
	default:
		return ""
	}
}

func validateSourceMIME(target targetEndpoint, mimeType string) error {
	if target.isDoubao() {
		switch mimeType {
		case "image/jpeg", "image/png", "image/webp", "image/bmp", "image/tiff", "image/gif", "image/heic", "image/heif":
			return nil
		}
		return fmt.Errorf("source image mime %q is not supported by Doubao Seedream", mimeType)
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
		return nil
	default:
		return fmt.Errorf("source image mime %q is not supported by OpenAI image edits", mimeType)
	}
}

func ensureImageFilename(filename, mimeType string) string {
	if filepath.Ext(filename) != "" {
		return filename
	}
	switch mimeType {
	case "image/jpeg":
		return filename + ".jpg"
	case "image/webp":
		return filename + ".webp"
	case "image/gif":
		return filename + ".gif"
	case "image/bmp":
		return filename + ".bmp"
	case "image/tiff":
		return filename + ".tiff"
	case "image/heic":
		return filename + ".heic"
	case "image/heif":
		return filename + ".heif"
	default:
		return filename + ".png"
	}
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

func latestSessionImageSource(sessionRoot string) (sourceImage, bool) {
	for _, dir := range []string{
		filepath.Join(sessionRoot, generatedDirName),
		filepath.Join(sessionRoot, attachmentsDir),
	} {
		if path, ok := latestImagePath(dir); ok {
			return sourceImage{Path: path}, true
		}
	}
	return sourceImage{}, false
}

func latestImagePath(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var latestPath string
	var latestMod time.Time
	for _, entry := range entries {
		if entry.IsDir() || !isSupportedImagePath(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if latestPath == "" || info.ModTime().After(latestMod) || (info.ModTime().Equal(latestMod) && entry.Name() > filepath.Base(latestPath)) {
			latestPath = filepath.Join(dir, entry.Name())
			latestMod = info.ModTime()
		}
	}
	return latestPath, latestPath != ""
}

func isSupportedImagePath(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func resultContent(images []GeneratedImage, modelID, mode string, sourceCount int, outDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "generated %d image(s) with %s; mode=%s; source_images_count=%d; files are available in %s", len(images), modelID, mode, sourceCount, outDir)
	for _, img := range images {
		fmt.Fprintf(&b, "\n- %s (%s, %d bytes", img.Path, img.MIME, img.Bytes)
		if img.Size != "" {
			fmt.Fprintf(&b, ", %s", img.Size)
		}
		b.WriteString(")")
	}
	b.WriteString("\nFor follow-up image edits, pass the relevant file path in source_images[].path.")
	return b.String()
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
