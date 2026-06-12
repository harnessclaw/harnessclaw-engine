package config

import (
	"testing"

	"go.uber.org/zap"
)

func videoCfg(apiKey string) VideoGenConfig {
	return VideoGenConfig{
		Providers: map[string]VideoProviderConfig{
			"doubao": {
				APIKey:  apiKey,
				BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
				Endpoints: map[string]VideoEndpointConfig{
					"seedance-lite-i2v": {Model: "doubao-seedance-1-0-lite-i2v-250428"},
				},
			},
		},
	}
}

func TestVideoEndpointExists(t *testing.T) {
	t.Parallel()
	c := &Config{VideoGen: videoCfg("sk-x")}
	if !c.videoEndpointExists("doubao:seedance-lite-i2v") {
		t.Fatal("valid ref should resolve")
	}
	if c.videoEndpointExists("doubao:missing") {
		t.Fatal("unknown endpoint must not resolve")
	}
	if c.videoEndpointExists("runway:x") {
		t.Fatal("unknown provider must not resolve")
	}
	if c.videoEndpointExists("garbage") {
		t.Fatal("unparseable ref must not resolve")
	}
	c2 := &Config{VideoGen: videoCfg("")}
	if c2.videoEndpointExists("doubao:seedance-lite-i2v") {
		t.Fatal("endpoint with empty api_key must not resolve")
	}
}

func TestSanitizeClearsStaleVideoGeneration(t *testing.T) {
	t.Parallel()
	c := &Config{VideoGen: videoCfg("sk-x")}
	c.Agent.VideoGeneration = "doubao:does-not-exist"
	c.SanitizeLLM(zap.NewNop())
	if c.Agent.VideoGeneration != "" {
		t.Fatalf("stale video_generation should be cleared, got %q", c.Agent.VideoGeneration)
	}

	c2 := &Config{VideoGen: videoCfg("sk-x")}
	c2.Agent.VideoGeneration = "doubao:seedance-lite-i2v"
	c2.SanitizeLLM(zap.NewNop())
	if c2.Agent.VideoGeneration != "doubao:seedance-lite-i2v" {
		t.Fatalf("valid video_generation should survive, got %q", c2.Agent.VideoGeneration)
	}
}
