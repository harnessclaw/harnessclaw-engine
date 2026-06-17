package imagegen

import (
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const (
	ToolName         = "image_generate"
	generatedDirName = "generated"
	defaultSize      = "2048x2048"
	defaultCount     = 1
	maxCount         = 4
	requestTimeout   = 5 * time.Minute
	tlsHandshakeWait = time.Minute
)

// imageSizes are the supported output resolutions: 2K / 3K / 4K across the
// common aspect ratios (1:1, 4:3, 3:4, 16:9, 9:16, 3:2, 2:3, 21:9). This is the
// single source of truth — both the InputSchema enum and allowedSizes derive
// from it.
var imageSizes = []string{
	// 2K
	"2048x2048", "2304x1728", "1728x2304", "2848x1600", "1600x2848", "2496x1664", "1664x2496", "3136x1344",
	// 3K
	"3072x3072", "3456x2592", "2592x3456", "4096x2304", "2304x4096", "2496x3744", "3744x2496", "4704x2016",
	// 4K
	"4096x4096", "3520x4704", "4704x3520", "5504x3040", "3040x5504", "3328x4992", "4992x3328", "6240x2656",
}

var allowedSizes = func() map[string]bool {
	m := make(map[string]bool, len(imageSizes))
	for _, s := range imageSizes {
		m[s] = true
	}
	return m
}()

// AgentConfigSource is satisfied by provider/manager.Manager and is used by
// Source to read the live agent.image_generation selector.
type AgentConfigSource interface {
	CurrentAgent() config.AgentConfig
}

type Tool struct {
	tool.BaseTool
	source   ImageGenSource
	registry *ProviderRegistry
	rootDir  string
	client   *http.Client // URL-fallback download only
	logger   *zap.Logger
	sleep    func(time.Duration)
}

type Option func(*Tool)

func WithHTTPClient(client *http.Client) Option {
	return func(t *Tool) {
		if client != nil {
			t.client = client
		}
	}
}

func New(source ImageGenSource, registry *ProviderRegistry, rootDir string, logger *zap.Logger, opts ...Option) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	t := &Tool{
		source:   source,
		registry: registry,
		rootDir:  rootDir,
		client:   newHTTPClient(),
		logger:   logger.Named("imagegen"),
		sleep:    time.Sleep,
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
	return "用文本 prompt 生成图片（同步），调用配置好的图片生成模型。返回生成图片的本地文件路径，图片已自动落到 task 目录下，不需要再 cp / mv 搬运。"
}
func (*Tool) IsReadOnly() bool              { return false }
func (*Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*Tool) IsConcurrencySafe() bool       { return true }
func (*Tool) IsLongRunning() bool           { return true }

func (t *Tool) IsEnabled() bool {
	if t.source == nil || t.registry == nil {
		return false
	}
	ref := t.source.AgentImageGeneration()
	if ref == "" {
		return false
	}
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return false
	}
	_, ok = t.registry.Get(ep.Provider)
	return ok
}

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
				"description": "Image resolution (2K/3K/4K across common aspect ratios). Default 2048x2048 (2K 1:1).",
				"enum":        imageSizes,
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

	ref := t.source.AgentImageGeneration()
	if ref == "" {
		return errResult("agent.image_generation is not configured; enable an image provider in Settings > 图片生成模型 and select it", types.ToolErrorInvalidInput), nil
	}
	// Optional explicit selector must match the configured ref (provider:endpoint).
	if sel := strings.TrimSpace(in.Model); sel != "" && sel != ref {
		return errResult(fmt.Sprintf("model %q does not match configured agent.image_generation %q", sel, ref), types.ToolErrorInvalidInput), nil
	}
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return errResult(fmt.Sprintf("configured agent.image_generation %q is not a usable image endpoint", ref), types.ToolErrorInvalidInput), nil
	}
	provider, ok := t.registry.Get(ep.Provider)
	if !ok {
		return errResult(fmt.Sprintf("image provider %q not implemented", ep.Provider), types.ToolErrorInternal), nil
	}

	sessionRoot, err := t.resolveSessionRoot(ctx)
	if err != nil {
		return errResult(err.Error(), types.ToolErrorInternal), nil
	}
	outDir := filepath.Join(sessionRoot, generatedDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return errResult("create generated directory: "+err.Error(), types.ToolErrorInternal), nil
	}

	result, err := t.generateWithRetry(ctx, provider, GenerateRequest{
		Endpoint: ep,
		Prompt:   in.Prompt,
		N:        in.N,
		Size:     in.Size,
		Quality:  in.Quality,
		Style:    in.Style,
	})
	if err != nil {
		return classifyImageError("image generation request failed", err), nil
	}

	images := make([]GeneratedImage, 0, len(result.Images))
	for idx, item := range result.Images {
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
			Model:  ep.Model,
			Prompt: prompt,
			Size:   in.Size,
		})
	}
	if len(images) == 0 {
		return errResult("image generation response did not include any images", types.ToolErrorModelError), nil
	}
	t.emitDeliverables(ctx, images)

	return &types.ToolResult{
		Content: fmt.Sprintf("generated %d image(s) with %s; files are available in %s", len(images), ep.Model, outDir),
		Metadata: map[string]any{
			"images":   images,
			"model":    ep.Model,
			"provider": ep.Provider,
			"endpoint": ep.Endpoint,
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

// classifyImageError maps a provider error's sentinel class to a ToolResult
// error type so the caller surfaces permission/validation/dependency failures
// distinctly.
func classifyImageError(prefix string, err error) *types.ToolResult {
	switch {
	case errors.Is(err, ErrPermissionDenied):
		return errResult(prefix+": "+err.Error(), types.ToolErrorPermissionDenied)
	case errors.Is(err, ErrValidation):
		return errResult(prefix+": "+err.Error(), types.ToolErrorInvalidInput)
	default:
		return errResult(prefix+": "+err.Error(), types.ToolErrorDependencyFail)
	}
}

// generateWithRetry calls the provider with exponential backoff. Permission and
// validation errors short-circuit; everything else (transient) is retried.
func (t *Tool) generateWithRetry(ctx context.Context, p ImageProvider, req GenerateRequest) (*GenerateResult, error) {
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		res, err := p.Generate(ctx, req)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrValidation) {
			return nil, err
		}
		if attempt < len(backoffs) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			t.sleep(backoffs[attempt])
		}
	}
	return nil, lastErr
}

func (t *Tool) resolveImageBytes(ctx context.Context, item GeneratedImageData) ([]byte, string, error) {
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

func errResult(msg string, errType types.ToolErrorType) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: errType}
}
