package doubao

import (
	"context"
	"net/http"
	"time"

	videogen "harnessclaw-go/internal/tools/builtin/videogen"
	"go.uber.org/zap"
)

// compile-time assertion that Provider satisfies the interface.
var _ videogen.VideoProvider = (*Provider)(nil)

// Provider implements videogen.VideoProvider against the Volcengine Ark API.
// The Volcengine video models (Seedance, branded "即梦"/Jimeng on the consumer
// side) all share this one Ark task API, so a single implementation registers
// under whatever provider key the operator configures (doubao / jimeng / ...).
type Provider struct {
	name   string
	client *client
	logger *zap.Logger
}

// NewProvider builds an Ark video provider registered under the given name.
// Empty name defaults to "doubao" for backward compatibility.
func NewProvider(name string, logger *zap.Logger) *Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	if name == "" {
		name = "doubao"
	}
	return &Provider{name: name, client: newClient(nil), logger: logger.Named("videogen." + name)}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) SubmitTask(ctx context.Context, req videogen.SubmitRequest) (*videogen.SubmitResult, error) {
	cr := creds{apiKey: req.Endpoint.APIKey, baseURL: req.Endpoint.BaseURL}
	id, err := p.client.submit(ctx, cr, arkSubmitBody{
		Model:     req.Endpoint.Model,
		Prompt:    req.Prompt,
		ImageURL:  req.ImageURL,
		ImageB64:  req.ImageB64,
		Ratio:     req.AspectRatio,
		Duration:  req.DurationS,
		Seed:      req.Seed,
		Watermark: false,
	})
	if err != nil {
		return nil, err
	}
	return &videogen.SubmitResult{TaskID: id, SubmittedAt: time.Now()}, nil
}

func (p *Provider) QueryTask(ctx context.Context, req videogen.QueryRequest) (*videogen.QueryResult, error) {
	cr := creds{apiKey: req.Endpoint.APIKey, baseURL: req.Endpoint.BaseURL}
	resp, code, err := p.client.query(ctx, cr, req.TaskID)
	if err != nil {
		// 404 is a normal terminal state, not a transport error.
		if code == http.StatusNotFound {
			return &videogen.QueryResult{Status: videogen.StatusNotFound}, nil
		}
		return nil, err
	}
	out := &videogen.QueryResult{
		Status:     mapStatus(resp.Status),
		Model:      resp.Model,
		Resolution: resp.Resolution,
		Ratio:      resp.Ratio,
		Duration:   resp.Duration,
	}
	if resp.Content != nil {
		out.VideoURL = resp.Content.VideoURL
	}
	if resp.UpdatedAt > 0 {
		out.URLExpiresAt = time.Unix(resp.UpdatedAt, 0).Add(24 * time.Hour)
	}
	if resp.Error != nil {
		out.ErrorCode = resp.Error.Code
		out.ErrorMessage = resp.Error.Message
	}
	return out, nil
}

func (p *Provider) DownloadVideo(ctx context.Context, url string) ([]byte, string, error) {
	return p.client.download(ctx, url)
}
