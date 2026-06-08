package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	modelregistry "harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

type staticConfigSource struct {
	cfg   config.LLMConfig
	agent config.AgentConfig
}

func (s staticConfigSource) CurrentConfig() config.LLMConfig  { return s.cfg }
func (s staticConfigSource) CurrentAgent() config.AgentConfig { return s.agent }

func TestValidateInputRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tr := New(staticConfigSource{}, testRegistry("http://example.test"), t.TempDir(), zap.NewNop())

	for name, raw := range map[string]string{
		"missing prompt": `{}`,
		"blank prompt":   `{"prompt":"   "}`,
		"too many":       `{"prompt":"cat","n":5}`,
		"bad size":       `{"prompt":"cat","size":"2048x2048"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := tr.ValidateInput(json.RawMessage(raw)); err == nil {
				t.Fatalf("ValidateInput(%s) succeeded, want error", raw)
			}
		})
	}
}

func TestExecuteRejectsNonImageGenerationModel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-non-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	tr := New(staticConfigSource{cfg: config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type:    "openai",
				BaseURL: "http://example.test",
				APIKey:  "sk-test",
				Endpoints: map[string]config.EndpointConfig{
					"gpt-5": {Model: "gpt-5"},
				},
			},
		},
	}}, testRegistry("http://example.test"), root, zap.NewNop())

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"cat","model":"openai:gpt-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want image-generation capability error: %#v", res)
	}
	if !strings.Contains(res.Content, "image_generation") {
		t.Fatalf("error content %q does not mention image_generation", res.Content)
	}
}

func TestExecuteRequiresConfiguredAgentImageGeneration(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-requires-agent-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	tr := New(staticConfigSource{cfg: imageConfig(srv.URL)}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	for name, raw := range map[string]string{
		"omitted model":  `{"prompt":"small cat"}`,
		"explicit model": `{"prompt":"small cat","model":"gpt-image:gpt-image-2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := tr.Execute(ctx, json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("Execute succeeded, want configuration error: %#v", res)
			}
			if !strings.Contains(res.Content, "agent.image_generation") {
				t.Fatalf("error content %q does not mention agent.image_generation", res.Content)
			}
		})
	}
}

func TestExecuteUsesArtifactProducerSessionWhenAgentScopeMissing(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-producer"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	tr := New(staticConfigSource{
		cfg:   imageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithArtifactProducer(context.Background(), tool.ArtifactProducer{SessionID: sid})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	images := res.Metadata["images"].([]GeneratedImage)
	if !strings.HasPrefix(images[0].Path, workspace.SessionRoot(root, sid)) {
		t.Fatalf("image path %q is not under producer session root", images[0].Path)
	}
}

func TestExecutePostsToImageEndpointAndWritesFiles(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":          []map[string]string{{"b64_json": pngB64, "revised_prompt": "small cat"}},
			"size":          "1024x1024",
			"quality":       "high",
			"output_format": "png",
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	events := make(chan types.EngineEvent, 1)
	tr := New(staticConfigSource{
		cfg:   imageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	ctx = tool.WithEventOut(ctx, events)
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat","model":"gpt-image:gpt-image-2","n":1,"size":"1024x1024","quality":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("request path = %q, want /v1/images/generations", gotPath)
	}
	if gotAuth != "Bearer sk-image" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["prompt"] != "small cat" || gotBody["model"] != "gpt-image-2" || gotBody["response_format"] != "b64_json" {
		t.Fatalf("unexpected request body: %#v", gotBody)
	}
	if strings.Contains(res.Content, pngB64) {
		t.Fatalf("tool content leaked base64")
	}

	images, ok := res.Metadata["images"].([]GeneratedImage)
	if !ok || len(images) != 1 {
		t.Fatalf("metadata images = %#v", res.Metadata["images"])
	}
	img := images[0]
	if img.MIME != "image/png" || img.Model != "gpt-image-2" || img.Prompt != "small cat" {
		t.Fatalf("unexpected image metadata: %#v", img)
	}
	if !strings.HasPrefix(img.Path, workspace.SessionRoot(root, sid)) {
		t.Fatalf("image path %q is not under session root", img.Path)
	}
	written, err := os.ReadFile(img.Path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	wantBytes, _ := base64.StdEncoding.DecodeString(pngB64)
	if string(written) != string(wantBytes) {
		t.Fatalf("written bytes mismatch")
	}
	if filepath.Base(filepath.Dir(img.Path)) != generatedDirName {
		t.Fatalf("image path %q not under generated dir", img.Path)
	}
	select {
	case evt := <-events:
		if evt.Type != types.EngineEventDeliverable || evt.Deliverable == nil || evt.Deliverable.FilePath != img.Path {
			t.Fatalf("unexpected deliverable event: %#v", evt)
		}
	default:
		t.Fatalf("missing deliverable event")
	}
}

func TestExecuteUsesConfiguredAgentImageGenerationEndpoint(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-agent-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	cfg := config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"aaa": {
				Type:    "openai",
				BaseURL: srv.URL,
				APIKey:  "sk-first",
				Endpoints: map[string]config.EndpointConfig{
					"first-image": {
						Model:     "first-image",
						ModelType: []string{"image_generation"},
					},
				},
			},
			"openai": {
				Type:    "openai",
				BaseURL: srv.URL,
				APIKey:  "sk-openai",
				Endpoints: map[string]config.EndpointConfig{
					"gpt-image-2": {
						Model:     "gpt-image-2",
						ModelType: []string{"vision", "image_generation"},
					},
				},
			},
		},
	}
	tr := New(staticConfigSource{
		cfg:   cfg,
		agent: config.AgentConfig{ImageGeneration: "openai:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotBody["model"] != "gpt-image-2" {
		t.Fatalf("request model = %v, want gpt-image-2", gotBody["model"])
	}
}

func TestExecuteRejectsDisabledAgentImageGenerationEndpoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-disabled-agent-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	cfg := config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type:    "openai",
				BaseURL: "http://example.test",
				APIKey:  "sk-openai",
				Endpoints: map[string]config.EndpointConfig{
					"gpt-image-2": {
						Model:     "gpt-image-2",
						Disabled:  true,
						ModelType: []string{"vision", "image_generation"},
					},
				},
			},
		},
	}
	tr := New(staticConfigSource{
		cfg:   cfg,
		agent: config.AgentConfig{ImageGeneration: "openai:gpt-image-2"},
	}, testRegistry("http://example.test"), root, zap.NewNop())

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded with disabled image endpoint: %#v", res)
	}
	if !strings.Contains(res.Content, "not available") {
		t.Fatalf("error content %q does not mention availability", res.Content)
	}
}

func imageConfig(baseURL string) config.LLMConfig {
	return config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"gpt-image": {
				Type:    "openai",
				BaseURL: baseURL,
				APIKey:  "sk-image",
				Endpoints: map[string]config.EndpointConfig{
					"gpt-image-2": {Model: "gpt-image-2"},
				},
			},
		},
	}
}

func testRegistry(baseURL string) *modelregistry.Registry {
	return modelregistry.NewRegistry(&modelregistry.Manifest{
		Providers: map[string]*modelregistry.ProviderSpec{
			"gpt-image": {
				BaseURL: baseURL,
				Auth: modelregistry.ProviderAuth{
					Type:      "bearer",
					KeyHeader: "Authorization",
					KeyPrefix: "Bearer ",
				},
				Endpoints: modelregistry.ProviderEndpoints{
					ImagesGenerations: strPtr("/v1/images/generations"),
				},
			},
			"openai": {
				BaseURL: baseURL,
				Auth: modelregistry.ProviderAuth{
					Type:      "bearer",
					KeyHeader: "Authorization",
					KeyPrefix: "Bearer ",
				},
				Endpoints: modelregistry.ProviderEndpoints{
					ImagesGenerations: strPtr("/v1/images/generations"),
				},
			},
		},
		Models: map[string]*modelregistry.ModelSpec{
			"gpt-image/gpt-image-2": {
				Provider: "gpt-image",
				ModelID:  "gpt-image-2",
				Supports: modelregistry.SupportsFlags{
					ImageGeneration: true,
				},
			},
			"openai/gpt-5": {
				Provider: "openai",
				ModelID:  "gpt-5",
			},
		},
	})
}

func strPtr(s string) *string { return &s }
