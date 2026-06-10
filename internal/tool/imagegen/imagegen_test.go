package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	modelregistry "harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

var _ tool.LongRunningTool = (*Tool)(nil)

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
		"bad size":       `{"prompt":"cat","size":"giant"}`,
		"bad mode":       `{"prompt":"cat","mode":"retouch"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := tr.ValidateInput(json.RawMessage(raw)); err == nil {
				t.Fatalf("ValidateInput(%s) succeeded, want error", raw)
			}
		})
	}
}

func TestNewUsesSlowImageGenerationTimeout(t *testing.T) {
	t.Parallel()

	tr := New(staticConfigSource{}, testRegistry("http://example.test"), t.TempDir(), zap.NewNop())

	if tr.client.Timeout != 5*time.Minute {
		t.Fatalf("default image generation timeout = %s, want 5m0s", tr.client.Timeout)
	}
}

func TestNewUsesSlowTLSHandshakeTimeout(t *testing.T) {
	t.Parallel()

	tr := New(staticConfigSource{}, testRegistry("http://example.test"), t.TempDir(), zap.NewNop())

	transport, ok := tr.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default image generation transport = %T, want *http.Transport", tr.client.Transport)
	}
	if transport.TLSHandshakeTimeout != time.Minute {
		t.Fatalf("default image generation TLS handshake timeout = %s, want 1m0s", transport.TLSHandshakeTimeout)
	}
}

func TestImageGenerateIsLongRunning(t *testing.T) {
	t.Parallel()

	tr := New(staticConfigSource{}, testRegistry("http://example.test"), t.TempDir(), zap.NewNop())

	if !tr.IsLongRunning() {
		t.Fatalf("image_generate must bypass executor timeout and use its own request timeout")
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
	if !strings.Contains(res.Content, img.Path) {
		t.Fatalf("tool result content should expose generated image path for follow-up edits: %q", res.Content)
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

func TestExecuteWithSourceImageUsesOpenAIEditsMultipart(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotPath, gotAuth, gotContentType string
	var gotFields map[string]string
	var gotFiles map[string][]multipartFilePart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotFields, gotFiles = readMultipartRequestDetails(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64, "revised_prompt": "edited cat"}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-openai-edit"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	sourcePath := writePNGFixture(t, root)
	tr := New(staticConfigSource{
		cfg:   imageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	raw := mustJSON(t, map[string]any{
		"prompt":             "edit cat",
		"model":              "gpt-image:gpt-image-2",
		"source_images":      []map[string]string{{"path": sourcePath}},
		"size":               "1024x1024",
		"quality":            "high",
		"style":              "vivid",
		"output_format":      "webp",
		"output_compression": 80,
	})
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("request path = %q, want /v1/images/edits", gotPath)
	}
	if gotAuth != "Bearer sk-image" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", gotContentType)
	}
	if gotFields["prompt"] != "edit cat" || gotFields["model"] != "gpt-image-2" || gotFields["response_format"] != "b64_json" {
		t.Fatalf("unexpected multipart fields: %#v", gotFields)
	}
	if gotFields["style"] != "" {
		t.Fatalf("GPT Image 2 edit request must not send style: %#v", gotFields)
	}
	if gotFields["output_format"] != "webp" || gotFields["output_compression"] != "80" {
		t.Fatalf("output fields missing: %#v", gotFields)
	}
	if len(gotFiles["image"]) != 1 {
		t.Fatalf("multipart image files = %#v, want one image", gotFiles)
	}
	if gotFiles["image"][0].ContentType != "image/png" {
		t.Fatalf("multipart image Content-Type = %q, want image/png", gotFiles["image"][0].ContentType)
	}
	if res.Metadata["source_images_count"] != 1 {
		t.Fatalf("source_images_count metadata = %#v", res.Metadata["source_images_count"])
	}
	if res.Metadata["mode"] != "edit" {
		t.Fatalf("mode metadata = %#v", res.Metadata["mode"])
	}
}

func TestExecuteWithCurrentImagesUsesAttachedImageInput(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotPath string
	var gotFields map[string]string
	var gotFiles map[string][]multipartFilePart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFields, gotFiles = readMultipartRequestDetails(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-attached-edit"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	tr := New(staticConfigSource{
		cfg:   imageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	ctx = tool.WithCurrentImages(ctx, []tool.CurrentImage{{
		MediaType: "application/octet-stream",
		Data:      base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}),
		Filename:  "attached.jpeg",
	}})
	res, err := tr.Execute(ctx, json.RawMessage(`{
		"prompt":"use attached backpack",
		"mode":"edit",
		"mask":{"path":"","url":""},
		"source_images":[],
		"output_format":"png",
		"output_compression":90,
		"use_attached_images":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("request path = %q, want /v1/images/edits", gotPath)
	}
	if len(gotFiles["image"]) != 1 {
		t.Fatalf("multipart image files = %#v, want one attached image", gotFiles)
	}
	if len(gotFiles["mask"]) != 0 {
		t.Fatalf("empty mask input should be omitted, got files %#v", gotFiles["mask"])
	}
	if gotFiles["image"][0].ContentType != "image/jpeg" {
		t.Fatalf("multipart image Content-Type = %q, want image/jpeg", gotFiles["image"][0].ContentType)
	}
	if gotFields["output_format"] != "png" {
		t.Fatalf("output_format = %q, want png", gotFields["output_format"])
	}
	if gotFields["output_compression"] != "" {
		t.Fatalf("png edit request must not send output_compression: %#v", gotFields)
	}
	if res.Metadata["source_images_count"] != 1 {
		t.Fatalf("source_images_count metadata = %#v", res.Metadata["source_images_count"])
	}
}

func TestExecuteEditFallsBackToLatestGeneratedImage(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotPath string
	var gotFiles map[string][]multipartFilePart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, gotFiles = readMultipartRequestDetails(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-edit-fallback"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	sessionRoot := workspace.SessionRoot(root, sid)
	generatedDir := filepath.Join(sessionRoot, generatedDirName)
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := writePNGFixtureAt(t, generatedDir, "old.png")
	newPath := writePNGFixtureAt(t, generatedDir, "new.png")
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	tr := New(staticConfigSource{
		cfg:   imageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, testRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: sessionRoot})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"edit previous image","mode":"edit"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("request path = %q, want /v1/images/edits", gotPath)
	}
	if len(gotFiles["image"]) != 1 {
		t.Fatalf("multipart image files = %#v, want one fallback image", gotFiles)
	}
	if gotFiles["image"][0].Filename != filepath.Base(newPath) {
		t.Fatalf("fallback image = %q, want latest %q", gotFiles["image"][0].Filename, filepath.Base(newPath))
	}
	if gotFiles["image"][0].ContentType != "image/png" {
		t.Fatalf("fallback image Content-Type = %q, want image/png", gotFiles["image"][0].ContentType)
	}
	if res.Metadata["source_images_count"] != 1 {
		t.Fatalf("source_images_count metadata = %#v", res.Metadata["source_images_count"])
	}
}

func TestExecuteWithDoubaoSourceImagesUsesGenerationImageField(t *testing.T) {
	t.Parallel()

	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": pngB64}},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	sid := "sess-doubao-edit"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	sourcePath := writePNGFixture(t, root)
	tr := New(staticConfigSource{
		cfg:   doubaoImageConfig(srv.URL),
		agent: config.AgentConfig{ImageGeneration: "doubao:seedream"},
	}, doubaoTestRegistry(srv.URL), root, zap.NewNop(), WithHTTPClient(srv.Client()))

	raw := mustJSON(t, map[string]any{
		"prompt": "merge references",
		"model":  "doubao:seedream",
		"source_images": []map[string]string{
			{"path": sourcePath},
			{"url": "https://example.test/ref.png"},
		},
		"n":             3,
		"size":          "2K",
		"output_format": "png",
		"watermark":     false,
	})
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	if gotPath != "/images/generations" {
		t.Fatalf("request path = %q, want /images/generations", gotPath)
	}
	if gotBody["n"] != nil {
		t.Fatalf("doubao request must not send n: %#v", gotBody)
	}
	refs, ok := gotBody["image"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("doubao image field = %#v, want two references", gotBody["image"])
	}
	if first, _ := refs[0].(string); !strings.HasPrefix(first, "data:image/png;base64,") {
		t.Fatalf("first image reference = %q, want data URL", refs[0])
	}
	if refs[1] != "https://example.test/ref.png" {
		t.Fatalf("second image reference = %#v", refs[1])
	}
	if gotBody["sequential_image_generation"] != "auto" {
		t.Fatalf("sequential_image_generation = %#v", gotBody["sequential_image_generation"])
	}
	opts, ok := gotBody["sequential_image_generation_options"].(map[string]any)
	if !ok || opts["max_images"] != float64(3) {
		t.Fatalf("sequential options = %#v", gotBody["sequential_image_generation_options"])
	}
	if gotBody["watermark"] != false {
		t.Fatalf("watermark = %#v, want false", gotBody["watermark"])
	}
}

func TestExecuteRejectsImageInputWhenEndpointLacksVision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-no-image-input"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	sourcePath := writePNGFixture(t, root)
	cfg := config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type:    "openai",
				BaseURL: "http://example.test",
				APIKey:  "sk-openai",
				Endpoints: map[string]config.EndpointConfig{
					"text-to-image": {
						Model:     "text-to-image",
						ModelType: []string{"image_generation"},
					},
				},
			},
		},
	}
	tr := New(staticConfigSource{
		cfg:   cfg,
		agent: config.AgentConfig{ImageGeneration: "openai:text-to-image"},
	}, testRegistry("http://example.test"), root, zap.NewNop())

	raw := mustJSON(t, map[string]any{
		"prompt":        "edit",
		"source_images": []map[string]string{{"path": sourcePath}},
	})
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want image-input capability error: %#v", res)
	}
	if !strings.Contains(res.Content, "does not support image input") {
		t.Fatalf("error content %q does not mention image input support", res.Content)
	}
}

func TestExecuteRejectsOpenAIImageInputWithoutImageEditsEndpoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-no-edits-endpoint"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	sourcePath := writePNGFixture(t, root)
	reg := testRegistry("http://example.test")
	reg.LookupProvider("gpt-image").Endpoints.ImageEdits = nil
	tr := New(staticConfigSource{
		cfg:   imageConfig("http://example.test"),
		agent: config.AgentConfig{ImageGeneration: "gpt-image:gpt-image-2"},
	}, reg, root, zap.NewNop())

	raw := mustJSON(t, map[string]any{
		"prompt":        "edit",
		"source_images": []map[string]string{{"path": sourcePath}},
	})
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want missing edits endpoint error: %#v", res)
	}
	if !strings.Contains(res.Content, "image_edits") {
		t.Fatalf("error content %q does not mention image_edits", res.Content)
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

func doubaoImageConfig(baseURL string) config.LLMConfig {
	return config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"doubao": {
				Type:    "doubao",
				BaseURL: baseURL,
				APIKey:  "sk-doubao",
				Endpoints: map[string]config.EndpointConfig{
					"seedream": {
						Model:     "doubao-seedream-5-0-260128",
						ModelType: []string{"vision", "image_generation"},
					},
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
					ImageEdits:        strPtr("/v1/images/edits"),
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
					ImageEdits:        strPtr("/v1/images/edits"),
				},
			},
		},
		Models: map[string]*modelregistry.ModelSpec{
			"gpt-image/gpt-image-2": {
				Provider: "gpt-image",
				ModelID:  "gpt-image-2",
				Supports: modelregistry.SupportsFlags{
					ImageGeneration: true,
					Vision:          true,
				},
			},
			"openai/gpt-5": {
				Provider: "openai",
				ModelID:  "gpt-5",
			},
		},
	})
}

func doubaoTestRegistry(baseURL string) *modelregistry.Registry {
	return modelregistry.NewRegistry(&modelregistry.Manifest{
		Providers: map[string]*modelregistry.ProviderSpec{
			"doubao": {
				BaseURL: baseURL,
				Auth: modelregistry.ProviderAuth{
					Type:      "bearer",
					KeyHeader: "Authorization",
					KeyPrefix: "Bearer ",
				},
				Endpoints: modelregistry.ProviderEndpoints{
					ImagesGenerations: strPtr("/images/generations"),
				},
			},
		},
		Models: map[string]*modelregistry.ModelSpec{
			"doubao/doubao-seedream-5-0-260128": {
				Provider: "doubao",
				ModelID:  "doubao-seedream-5-0-260128",
				Supports: modelregistry.SupportsFlags{
					ImageGeneration: true,
					Vision:          true,
				},
			},
		},
	})
}

func writePNGFixture(t *testing.T, dir string) string {
	t.Helper()
	return writePNGFixtureAt(t, dir, "source.png")
}

func writePNGFixtureAt(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readMultipartRequest(t *testing.T, r *http.Request) (map[string]string, map[string]int) {
	t.Helper()
	fields, parts := readMultipartRequestDetails(t, r)
	files := map[string]int{}
	for name, items := range parts {
		files[name] = len(items)
	}
	return fields, files
}

type multipartFilePart struct {
	Filename    string
	ContentType string
}

func readMultipartRequestDetails(t *testing.T, r *http.Request) (map[string]string, map[string][]multipartFilePart) {
	t.Helper()
	reader, err := r.MultipartReader()
	if err != nil {
		t.Fatalf("multipart reader: %v", err)
	}
	fields := map[string]string{}
	files := map[string][]multipartFilePart{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		name := part.FormName()
		if part.FileName() != "" {
			files[name] = append(files[name], multipartFilePart{
				Filename:    part.FileName(),
				ContentType: part.Header.Get("Content-Type"),
			})
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read form field: %v", err)
		}
		fields[name] = string(body)
	}
	return fields, files
}

func strPtr(s string) *string { return &s }
