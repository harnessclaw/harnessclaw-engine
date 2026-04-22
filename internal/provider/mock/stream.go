package mock

import (
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// BuildStream constructs a ChatStream from a Response.
// It creates a channel of StreamEvents and populates it synchronously.
func BuildStream(resp Response) *provider.ChatStream {
	ch := make(chan types.StreamEvent, 10+len(resp.ToolCalls))

	// Emit text if present.
	if resp.Text != "" {
		ch <- types.StreamEvent{
			Type: types.StreamEventText,
			Text: resp.Text,
		}
	}

	// Emit tool calls.
	for i := range resp.ToolCalls {
		ch <- types.StreamEvent{
			Type:     types.StreamEventToolUse,
			ToolCall: &resp.ToolCalls[i],
		}
	}

	// Determine stop reason.
	stopReason := resp.StopReason
	if stopReason == "" {
		if len(resp.ToolCalls) > 0 {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}

	// Default usage if not specified.
	usage := resp.Usage
	if usage == nil {
		usage = &types.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		}
	}

	// Emit message_end.
	ch <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: stopReason,
		Usage:      usage,
	}

	close(ch)

	return &provider.ChatStream{
		Events: ch,
		Err:    func() error { return nil },
	}
}

// TextResponse is a convenience constructor for a simple text response.
func TextResponse(text string) Response {
	return Response{Text: text}
}

// ToolResponse creates a response with tool calls.
func ToolResponse(text string, calls ...types.ToolCall) Response {
	return Response{
		Text:      text,
		ToolCalls: calls,
	}
}

// ErrorResponse creates a response that fails with an error.
func ErrorResponse(err error) Response {
	return Response{Error: err}
}
