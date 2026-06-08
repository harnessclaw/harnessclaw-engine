package router

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/pkg/types"
)

// TestRouter_RespectsEndpointOverride demonstrates the override flow at
// the router boundary: with vision effectively enabled (via stubModelInfo
// standing in for the endpoint-override-aware bridge), image content
// passes through. This is the contract C4 must preserve — the router
// doesn't know about model_type at all; it consumes whatever
// ActiveSupports() returns.
func TestRouter_RespectsEndpointOverride(t *testing.T) {
	eng := &captureEngine{}
	ch := &recordingChannel{}
	info := stubModelInfo{
		key:      "anthropic:x",
		supports: registry.SupportsFlags{Vision: true}, // simulating override result
	}
	r := New(eng, map[string]channel.Duplex{"websocket": ch}, nil, info, zap.NewNop())
	err := r.Handle(context.Background(), &types.IncomingMessage{
		ChannelName: "websocket", SessionID: "s",
		Content: []types.IncomingContentBlock{
			{Type: "image", MIMEType: "image/png", Data: "AA=="},
		},
	})
	if err != nil {
		t.Fatalf("override result with Vision=true should let image through: %v", err)
	}
}
