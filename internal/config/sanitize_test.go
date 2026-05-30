package config

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestSanitizeLLM_DropsUnknownType(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"ok": {
			Type: "openai", BaseURL: "x", APIKey: "x",
			Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}},
		},
		"bad-type": {
			Type: "deepseek", BaseURL: "x", APIKey: "x",
			Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}},
		},
		"empty-type": {
			Type: "", BaseURL: "x", APIKey: "x",
			Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}},
		},
	}
	c.SanitizeLLM(zap.NewNop())
	if _, ok := c.LLM.Providers["bad-type"]; ok {
		t.Errorf("bad-type provider should have been dropped")
	}
	if _, ok := c.LLM.Providers["empty-type"]; ok {
		t.Errorf("empty-type provider should have been dropped")
	}
	if _, ok := c.LLM.Providers["ok"]; !ok {
		t.Errorf("ok provider should survive")
	}
}

func TestSanitizeLLM_DropsBadProviderName(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"good": {Type: "openai", Endpoints: map[string]EndpointConfig{"e": {Model: "m"}}},
		"bad.dot": {Type: "openai", Endpoints: map[string]EndpointConfig{"e": {Model: "m"}}},
		"bad:colon": {Type: "openai", Endpoints: map[string]EndpointConfig{"e": {Model: "m"}}},
	}
	c.SanitizeLLM(zap.NewNop())
	if _, ok := c.LLM.Providers["bad.dot"]; ok {
		t.Errorf("provider name with . should be dropped")
	}
	if _, ok := c.LLM.Providers["bad:colon"]; ok {
		t.Errorf("provider name with : should be dropped")
	}
	if _, ok := c.LLM.Providers["good"]; !ok {
		t.Errorf("good provider should survive")
	}
}

func TestSanitizeLLM_DropsBadEndpoints(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"alpha": {
			Type: "openai",
			Endpoints: map[string]EndpointConfig{
				"valid":     {Model: "m1"},
				"empty":     {Model: ""},        // missing model
				"with:colon": {Model: "m2"},     // illegal name
				// 'gpt-5.5' SHOULD survive (dot allowed in endpoint names)
				"gpt-5.5": {Model: "gpt-5.5"},
			},
		},
	}
	c.SanitizeLLM(zap.NewNop())
	eps := c.LLM.Providers["alpha"].Endpoints
	if _, ok := eps["empty"]; ok {
		t.Errorf("empty-model endpoint should be dropped")
	}
	if _, ok := eps["with:colon"]; ok {
		t.Errorf("endpoint name with : should be dropped")
	}
	if _, ok := eps["valid"]; !ok {
		t.Errorf("valid endpoint should survive")
	}
	if _, ok := eps["gpt-5.5"]; !ok {
		t.Errorf("endpoint name with . SHOULD survive")
	}
}

func TestSanitizeLLM_DropsChainEntries(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"alpha": {Type: "openai", Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}}},
	}
	c.Agent.Primary = "alpha:ep"
	c.Agent.FallbackChain = []string{
		"missing-separator", // parse error
		"alpha:ghost",       // unknown endpoint
		"ghost:ep",          // unknown provider
		"alpha.ep",          // legacy . separator OK (resolves to alpha/ep)
	}
	c.SanitizeLLM(zap.NewNop())
	if c.Agent.Primary != "alpha:ep" {
		t.Fatalf("primary = %q, want alpha:ep", c.Agent.Primary)
	}
	want := []string{"alpha.ep"}
	if len(c.Agent.FallbackChain) != len(want) {
		t.Fatalf("chain = %v, want %v", c.Agent.FallbackChain, want)
	}
	for i, w := range want {
		if c.Agent.FallbackChain[i] != w {
			t.Errorf("chain[%d] = %q, want %q", i, c.Agent.FallbackChain[i], w)
		}
	}
}

func TestSanitizeLLM_ChainEntryPointingToJustDroppedProvider(t *testing.T) {
	// Provider has bad type → dropped; chain entry referencing it
	// should also be dropped in the same call.
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"alpha": {Type: "openai", Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}}},
		"junk":  {Type: "deepseek", Endpoints: map[string]EndpointConfig{"x": {Model: "m"}}},
	}
	c.Agent.Primary = "alpha:ep"
	c.Agent.FallbackChain = []string{"junk:x"}
	c.SanitizeLLM(zap.NewNop())
	if c.Agent.Primary != "alpha:ep" {
		t.Fatalf("primary = %q, want alpha:ep", c.Agent.Primary)
	}
	if len(c.Agent.FallbackChain) != 0 {
		t.Fatalf("chain = %v, want []", c.Agent.FallbackChain)
	}
}

func TestSanitizeLLM_ClearsBadPrimary(t *testing.T) {
	// agent.primary pointing at a dropped provider should be cleared.
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"alpha": {Type: "openai", Endpoints: map[string]EndpointConfig{"ep": {Model: "m"}}},
	}
	c.Agent.Primary = "ghost:ep"
	c.SanitizeLLM(zap.NewNop())
	if c.Agent.Primary != "" {
		t.Fatalf("primary = %q, want empty", c.Agent.Primary)
	}
}

func TestSanitizeLLM_DropsFallbackEntryDuplicatingPrimary(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"alpha": {Type: "openai", Endpoints: map[string]EndpointConfig{
			"ep1": {Model: "m1"}, "ep2": {Model: "m2"},
		}},
	}
	c.Agent.Primary = "alpha:ep1"
	c.Agent.FallbackChain = []string{"alpha:ep1", "alpha:ep2"}
	c.SanitizeLLM(zap.NewNop())
	want := []string{"alpha:ep2"}
	if len(c.Agent.FallbackChain) != len(want) || c.Agent.FallbackChain[0] != want[0] {
		t.Fatalf("chain = %v, want %v", c.Agent.FallbackChain, want)
	}
}

func TestSanitizeLLM_NilLoggerSafe(t *testing.T) {
	c := &Config{}
	c.LLM.Providers = map[string]ProviderConfig{
		"bad": {Type: "deepseek"},
	}
	c.SanitizeLLM(nil) // must not panic
	if _, ok := c.LLM.Providers["bad"]; ok {
		t.Errorf("sanitize still ran with nil logger")
	}
}

func TestSanitizeLLM_GroupFreeFormPreserved(t *testing.T) {
	longVal := strings.Repeat("x", 4096) // arbitrary long value — just to assert sanitize doesn't care about length
	cfg := &Config{
		LLM: LLMConfig{
			Providers: map[string]ProviderConfig{
				"openai": {
					Type:    "openai",
					BaseURL: "https://api.openai.com",
					APIKey:  "sk-x",
					Endpoints: map[string]EndpointConfig{
						"empty":      {Model: "m", Group: ""},
						"chinese":    {Model: "m", Group: "通义系列"},
						"with-space": {Model: "m", Group: "GPT 5 series"},
						"long":       {Model: "m", Group: longVal},
					},
				},
			},
		},
	}
	cfg.SanitizeLLM(zap.NewNop())
	got := cfg.LLM.Providers["openai"].Endpoints
	want := map[string]string{
		"empty":      "",
		"chinese":    "通义系列",
		"with-space": "GPT 5 series",
		"long":       longVal,
	}
	for name, wantGroup := range want {
		ep, ok := got[name]
		if !ok {
			t.Errorf("endpoint %q dropped — group value should be irrelevant to sanitize", name)
			continue
		}
		if ep.Group != wantGroup {
			t.Errorf("endpoint %q: Group mutated by sanitize: got %q, want %q", name, ep.Group, wantGroup)
		}
	}
}
