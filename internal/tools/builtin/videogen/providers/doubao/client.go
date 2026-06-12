package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

type creds struct {
	apiKey  string
	baseURL string // "" → defaultBaseURL
}

func (c creds) base() string {
	if strings.TrimSpace(c.baseURL) == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(c.baseURL, "/")
}

type client struct {
	http *http.Client
}

func newClient(h *http.Client) *client {
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	return &client{http: h}
}

// --- Ark wire types ---

type arkContentItem struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *arkImageURL `json:"image_url,omitempty"`
}

type arkImageURL struct {
	URL string `json:"url"`
}

type arkSubmitBody struct {
	Model     string
	Prompt    string
	ImageURL  string // wins over ImageB64
	ImageB64  string
	Ratio     string
	Duration  int
	Seed      *int
	Watermark bool
}

// marshalable form of the submit request.
type arkSubmitWire struct {
	Model     string           `json:"model"`
	Content   []arkContentItem `json:"content"`
	Ratio     string           `json:"ratio,omitempty"`
	Duration  int              `json:"duration,omitempty"`
	Watermark bool             `json:"watermark"`
	Seed      *int             `json:"seed,omitempty"`
}

type arkSubmitResponse struct {
	ID    string    `json:"id"`
	Error *arkError `json:"error,omitempty"`
}

type arkQueryResponse struct {
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	Error     *arkError `json:"error,omitempty"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `json:"updated_at"`
	Content   *struct {
		VideoURL     string `json:"video_url"`
		LastFrameURL string `json:"last_frame_url,omitempty"`
	} `json:"content,omitempty"`
	Resolution string `json:"resolution"`
	Ratio      string `json:"ratio"`
	Duration   int    `json:"duration"`
}

type arkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (b arkSubmitBody) wire() arkSubmitWire {
	content := []arkContentItem{}
	if strings.TrimSpace(b.Prompt) != "" {
		content = append(content, arkContentItem{Type: "text", Text: b.Prompt})
	}
	img := b.ImageURL
	if img == "" {
		img = b.ImageB64
	}
	if img != "" {
		content = append(content, arkContentItem{Type: "image_url", ImageURL: &arkImageURL{URL: img}})
	}
	return arkSubmitWire{
		Model:     b.Model,
		Content:   content,
		Ratio:     b.Ratio,
		Duration:  b.Duration,
		Watermark: b.Watermark,
		Seed:      b.Seed,
	}
}

func (c *client) submit(ctx context.Context, cr creds, body arkSubmitBody) (string, error) {
	payload, err := json.Marshal(body.wire())
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cr.base()+"/contents/generations/tasks", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cr.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var parsed arkSubmitResponse
	_ = json.Unmarshal(data, &parsed)
	if resp.StatusCode >= 400 || parsed.Error != nil {
		return "", arkHTTPError(resp.StatusCode, parsed.Error, data)
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("doubao: submit response missing id: %s", string(data))
	}
	return parsed.ID, nil
}

func (c *client) query(ctx context.Context, cr creds, taskID string) (*arkQueryResponse, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cr.base()+"/contents/generations/tasks/"+taskID, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cr.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var parsed arkQueryResponse
	uerr := json.Unmarshal(data, &parsed)
	if resp.StatusCode >= 400 {
		return &parsed, resp.StatusCode, arkHTTPError(resp.StatusCode, parsed.Error, data)
	}
	if uerr != nil {
		return nil, resp.StatusCode, fmt.Errorf("doubao: malformed query response (status %d): %w", resp.StatusCode, uerr)
	}
	return &parsed, resp.StatusCode, nil
}

func (c *client) download(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("doubao: download %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, "video/mp4", nil
}
