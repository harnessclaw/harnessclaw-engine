package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

const defaultPath = "/v1/images/generations"

type creds struct {
	apiKey     string
	baseURL    string
	path       string
	authHeader string // "" → Authorization
	authPrefix string // "" → "Bearer "
}

// url builds the request endpoint. When path is set it's joined to base_url
// (legacy split form); when path is empty base_url is treated as the complete
// endpoint URL (the unified "API 地址" form the client now uses). The default
// path is only applied when base_url has no path component of its own.
func (c creds) url() string {
	base := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	p := strings.TrimSpace(c.path)
	if p == "" {
		// base_url is the full endpoint URL already (e.g.
		// https://api.openai.com/v1/images/generations). Only fall back to
		// the default path when base_url is a bare origin with no path.
		if u, err := neturl.Parse(base); err == nil && strings.Trim(u.Path, "/") == "" {
			return base + defaultPath
		}
		return base
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

func (c creds) header() (string, string) {
	h := c.authHeader
	if h == "" {
		h = "Authorization"
	}
	pfx := c.authPrefix
	if pfx == "" {
		pfx = "Bearer "
	}
	return h, pfx
}

type client struct{ http *http.Client }

func newClient(h *http.Client) *client {
	if h == nil {
		h = &http.Client{Timeout: 120 * time.Second}
	}
	return &client{http: h}
}

type genBody struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
}

type genResponse struct {
	Data  []genImage `json:"data"`
	Error *genError  `json:"error,omitempty"`
}
type genImage struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
	MIME          string `json:"mime_type"`
}
type genError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// generate POSTs the request and returns the parsed response + HTTP status.
func (c *client) generate(ctx context.Context, cr creds, body genBody) (*genResponse, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cr.url(), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cr.apiKey != "" {
		h, pfx := cr.header()
		req.Header.Set(h, pfx+cr.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	var parsed genResponse
	uerr := json.Unmarshal(data, &parsed)
	if resp.StatusCode >= 400 {
		return &parsed, resp.StatusCode, classifyHTTP(resp.StatusCode, parsed.Error, data)
	}
	if uerr != nil {
		return nil, resp.StatusCode, fmt.Errorf("openai-image: malformed response (status %d): %w", resp.StatusCode, uerr)
	}
	return &parsed, resp.StatusCode, nil
}

// download fetches a remote image URL (when a provider returns url instead of b64).
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
		return nil, "", fmt.Errorf("openai-image: download %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, "", err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	return b, mime, nil
}
