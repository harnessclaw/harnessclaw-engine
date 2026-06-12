package config

import (
	"testing"

	"go.uber.org/zap"
)

func imgCfg(apiKey string) ImageGenConfig {
	return ImageGenConfig{
		Providers: map[string]ImageProviderConfig{
			"openai": {
				APIKey:  apiKey,
				BaseURL: "https://api.openai.com",
				Path:    "/v1/images/generations",
				Endpoints: map[string]ImageEndpointConfig{
					"gpt-image": {Model: "gpt-image-1"},
				},
			},
		},
	}
}

func TestImageEndpointExists(t *testing.T) {
	t.Parallel()
	c := &Config{ImageGen: imgCfg("sk-x")}
	if !c.imageEndpointExists("openai:gpt-image") {
		t.Fatal("valid ref should resolve")
	}
	if c.imageEndpointExists("openai:missing") {
		t.Fatal("unknown endpoint must not resolve")
	}
	if c.imageEndpointExists("nope:x") {
		t.Fatal("unknown provider must not resolve")
	}
	if c.imageEndpointExists("garbage") {
		t.Fatal("unparseable must not resolve")
	}
	c2 := &Config{ImageGen: imgCfg("")}
	if c2.imageEndpointExists("openai:gpt-image") {
		t.Fatal("empty api_key must not resolve")
	}
}

func TestSanitizeImageGenerationAgainstImageGen(t *testing.T) {
	t.Parallel()
	// Stale ref (not in cfg.ImageGen) is cleared.
	c := &Config{ImageGen: imgCfg("sk-x")}
	c.Agent.ImageGeneration = "openai:does-not-exist"
	c.SanitizeLLM(zap.NewNop())
	if c.Agent.ImageGeneration != "" {
		t.Fatalf("stale image_generation should be cleared, got %q", c.Agent.ImageGeneration)
	}
	// Valid cfg.ImageGen ref survives.
	c2 := &Config{ImageGen: imgCfg("sk-x")}
	c2.Agent.ImageGeneration = "openai:gpt-image"
	c2.SanitizeLLM(zap.NewNop())
	if c2.Agent.ImageGeneration != "openai:gpt-image" {
		t.Fatalf("valid image_generation should survive, got %q", c2.Agent.ImageGeneration)
	}
}
