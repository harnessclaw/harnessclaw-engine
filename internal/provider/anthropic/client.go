// Package anthropic implements provider.Provider for the Anthropic Messages API.
// Supports both direct Anthropic endpoints and compatible proxies (e.g. MaaS gateways).
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// Config holds Anthropic API connection parameters.
type Config struct {
	BaseURL string `mapstructure:"base_url" json:"base_url"`
	APIKey  string `mapstructure:"api_key"  json:"api_key"`
	Model   string `mapstructure:"model"    json:"model"`
}

// Client implements provider.Provider for the Anthropic Messages API.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates an Anthropic provider client.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{}}
}

func (c *Client) Name() string { return "anthropic" }

func (c *Client) CountTokens(_ context.Context, msgs []types.Message) (int, error) {
	total := 0
	for _, m := range msgs {
		for _, cb := range m.Content {
			total += len(cb.Text) + len(cb.ToolInput) + len(cb.ToolResult)
		}
	}
	return total / 4, nil // rough estimate
}

// Chat sends a streaming request to the Anthropic Messages API.
func (c *Client) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	model := c.cfg.Model
	if req.Model != "" {
		model = req.Model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	apiReq := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if req.System != "" {
		apiReq["system"] = req.System
	}
	if req.Temperature > 0 {
		apiReq["temperature"] = req.Temperature
	}

	apiReq["messages"] = convertMessages(req.Messages)

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			}
		}
		apiReq["tools"] = tools
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	eventsCh := make(chan types.StreamEvent, 64)
	var streamErr error

	go func() {
		defer close(eventsCh)
		defer resp.Body.Close()
		streamErr = parseSSE(resp.Body, eventsCh)
	}()

	return &provider.ChatStream{
		Events: eventsCh,
		Err:    func() error { return streamErr },
	}, nil
}

// ---------- Message conversion ----------

func convertMessages(msgs []types.Message) []map[string]any {
	var result []map[string]any
	for _, msg := range msgs {
		if msg.Role == types.RoleSystem {
			continue
		}
		var blocks []map[string]any
		for _, cb := range msg.Content {
			switch cb.Type {
			case types.ContentTypeText:
				if cb.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": cb.Text})
				}
			case types.ContentTypeToolUse:
				input := json.RawMessage(cb.ToolInput)
				if !json.Valid(input) {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": cb.ToolUseID, "name": cb.ToolName, "input": input,
				})
			case types.ContentTypeToolResult:
				block := map[string]any{"type": "tool_result", "tool_use_id": cb.ToolUseID}
				if cb.ToolResult != "" {
					block["content"] = cb.ToolResult
				}
				if cb.IsError {
					block["is_error"] = true
				}
				blocks = append(blocks, block)
			}
		}
		if len(blocks) > 0 {
			result = append(result, map[string]any{"role": string(msg.Role), "content": blocks})
		}
	}
	return result
}

// ---------- SSE stream parsing ----------

// toolBlock tracks an in-progress tool_use content block.
type toolBlock struct {
	id    string
	name  string
	input strings.Builder
}

func parseSSE(r io.Reader, ch chan<- types.StreamEvent) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	blocks := make(map[int]*toolBlock)
	var inputTokens, outputTokens int
	var stopReason string

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var base struct{ Type string `json:"type"` }
		if err := json.Unmarshal([]byte(data), &base); err != nil {
			continue
		}

		switch base.Type {
		case "message_start":
			var evt struct {
				Message struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			json.Unmarshal([]byte(data), &evt)
			inputTokens = evt.Message.Usage.InputTokens

		case "content_block_start":
			var evt struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			json.Unmarshal([]byte(data), &evt)
			if evt.ContentBlock.Type == "tool_use" {
				blocks[evt.Index] = &toolBlock{id: evt.ContentBlock.ID, name: evt.ContentBlock.Name}
			}

		case "content_block_delta":
			var evt struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			json.Unmarshal([]byte(data), &evt)

			switch evt.Delta.Type {
			case "text_delta":
				if evt.Delta.Text != "" {
					ch <- types.StreamEvent{Type: types.StreamEventText, Text: evt.Delta.Text}
				}
			case "input_json_delta":
				if tb, ok := blocks[evt.Index]; ok {
					tb.input.WriteString(evt.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			var evt struct{ Index int `json:"index"` }
			json.Unmarshal([]byte(data), &evt)

			if tb, ok := blocks[evt.Index]; ok {
				inputStr := tb.input.String()
				if inputStr == "" {
					inputStr = "{}"
				}
				ch <- types.StreamEvent{
					Type:     types.StreamEventToolUse,
					ToolCall: &types.ToolCall{ID: tb.id, Name: tb.name, Input: inputStr},
				}
				delete(blocks, evt.Index)
			}

		case "message_delta":
			var evt struct {
				Delta struct{ StopReason string `json:"stop_reason"` } `json:"delta"`
				Usage struct{ OutputTokens int `json:"output_tokens"` } `json:"usage"`
			}
			json.Unmarshal([]byte(data), &evt)
			stopReason = evt.Delta.StopReason
			outputTokens = evt.Usage.OutputTokens

		case "message_stop":
			ch <- types.StreamEvent{
				Type:       types.StreamEventMessageEnd,
				StopReason: stopReason,
				Usage:      &types.Usage{InputTokens: inputTokens, OutputTokens: outputTokens},
			}

		case "error":
			var evt struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal([]byte(data), &evt)
			ch <- types.StreamEvent{
				Type:  types.StreamEventError,
				Error: fmt.Errorf("API error (%s): %s", evt.Error.Type, evt.Error.Message),
			}

		case "ping":
			// ignore
		}
	}
	return scanner.Err()
}
