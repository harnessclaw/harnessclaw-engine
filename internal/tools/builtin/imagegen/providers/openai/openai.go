package openai

import (
	"context"

	imagegen "harnessclaw-go/internal/tools/builtin/imagegen"
	"go.uber.org/zap"
)

var _ imagegen.ImageProvider = (*Provider)(nil)

// Provider is the generic OpenAI-compatible synchronous image provider. It
// covers any /images/generations endpoint (openai, doubao Seedream, deepseek).
// Registered under each configured provider name (the base_url/path/key make
// it target-specific at call time).
type Provider struct {
	name   string
	client *client
	logger *zap.Logger
}

// NewProvider builds a generic provider registered under the given name.
func NewProvider(name string, logger *zap.Logger) *Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Provider{name: name, client: newClient(nil), logger: logger.Named("imagegen." + name)}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Generate(ctx context.Context, req imagegen.GenerateRequest) (*imagegen.GenerateResult, error) {
	cr := creds{
		apiKey:     req.Endpoint.APIKey,
		baseURL:    req.Endpoint.BaseURL,
		path:       req.Endpoint.Path,
		authHeader: req.Endpoint.AuthHeader,
		authPrefix: req.Endpoint.AuthPrefix,
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	resp, _, err := p.client.generate(ctx, cr, genBody{
		Model:          req.Endpoint.Model,
		Prompt:         req.Prompt,
		N:              n,
		Size:           req.Size,
		ResponseFormat: "b64_json",
		Quality:        req.Quality,
		Style:          req.Style,
	})
	if err != nil {
		return nil, err
	}
	out := &imagegen.GenerateResult{Images: make([]imagegen.GeneratedImageData, 0, len(resp.Data))}
	for _, im := range resp.Data {
		out.Images = append(out.Images, imagegen.GeneratedImageData{
			B64JSON:       im.B64JSON,
			URL:           im.URL,
			RevisedPrompt: im.RevisedPrompt,
			MIME:          im.MIME,
		})
	}
	return out, nil
}

// Download fetches a remote image URL (exposed for the tool's URL fallback).
func (p *Provider) Download(ctx context.Context, url string) ([]byte, string, error) {
	return p.client.download(ctx, url)
}
