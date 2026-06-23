package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

var _ tool.LongRunningTool = (*Tool)(nil)

const testPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="

// stubImageProvider is a canned ImageProvider for tool tests. It records the
// request it received and returns either result or err.
type stubImageProvider struct {
	name    string
	result  *GenerateResult
	err     error
	calls   int
	lastReq GenerateRequest
}

func (s *stubImageProvider) Name() string { return s.name }

func (s *stubImageProvider) Generate(_ context.Context, req GenerateRequest) (*GenerateResult, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

// newToolWith builds a Tool from a source (over cfg.ImageGen) + a registry
// holding the given provider. sleep is stubbed to a no-op so retry tests run
// instantly.
func newToolWith(t *testing.T, source ImageGenSource, provider ImageProvider, rootDir string) *Tool {
	t.Helper()
	reg := NewProviderRegistry()
	if provider != nil {
		if err := reg.Register(provider); err != nil {
			t.Fatalf("register provider: %v", err)
		}
	}
	tr := New(source, reg, rootDir, zap.NewNop())
	tr.sleep = func(time.Duration) {}
	return tr
}

// pngResult returns a one-image GenerateResult with the canned base64 PNG.
func pngResult(revised string) *GenerateResult {
	return &GenerateResult{Images: []GeneratedImageData{{B64JSON: testPNGB64, RevisedPrompt: revised}}}
}

func TestValidateInputRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	tr := newToolWith(t, source, &stubImageProvider{name: "openai"}, t.TempDir())

	for name, raw := range map[string]string{
		"missing prompt": `{}`,
		"blank prompt":   `{"prompt":"   "}`,
		"too many":       `{"prompt":"cat","n":5}`,
		"bad size":       `{"prompt":"cat","size":"100x100"}`,
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

	tr := New(nil, nil, t.TempDir(), zap.NewNop())
	if tr.client.Timeout != 5*time.Minute {
		t.Fatalf("default image generation timeout = %s, want 5m0s", tr.client.Timeout)
	}
	transport, ok := tr.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport = %T, want *http.Transport", tr.client.Transport)
	}
	if transport.TLSHandshakeTimeout != time.Minute {
		t.Fatalf("default TLS handshake timeout = %s, want 1m0s", transport.TLSHandshakeTimeout)
	}
}

func TestImageGenerateIsLongRunning(t *testing.T) {
	t.Parallel()

	tr := New(nil, nil, t.TempDir(), zap.NewNop())
	if !tr.IsLongRunning() {
		t.Fatalf("image_generate must bypass executor timeout and use its own request timeout")
	}
}

func TestIsEnabled(t *testing.T) {
	t.Parallel()

	usable := newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations")

	t.Run("nil source/registry", func(t *testing.T) {
		t.Parallel()
		tr := New(nil, nil, t.TempDir(), zap.NewNop())
		if tr.IsEnabled() {
			t.Fatal("IsEnabled() = true, want false for nil source/registry")
		}
	})

	cases := map[string]struct {
		source   ImageGenSource
		provider ImageProvider
		want     bool
	}{
		"empty agent ref": {
			source:   NewSource(usable, fakeAgentSource{ref: ""}),
			provider: &stubImageProvider{name: "openai"},
			want:     false,
		},
		"ref does not resolve": {
			source:   NewSource(newTestImageCfg("", "", ""), fakeAgentSource{ref: "openai:gpt-image"}),
			provider: &stubImageProvider{name: "openai"},
			want:     false,
		},
		"provider not registered": {
			source:   NewSource(usable, fakeAgentSource{ref: "openai:gpt-image"}),
			provider: &stubImageProvider{name: "someone-else"},
			want:     false,
		},
		"fully usable": {
			source:   NewSource(usable, fakeAgentSource{ref: "openai:gpt-image"}),
			provider: &stubImageProvider{name: "openai"},
			want:     true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := newToolWith(t, tc.source, tc.provider, t.TempDir())
			if got := tr.IsEnabled(); got != tc.want {
				t.Fatalf("IsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecuteRequiresConfiguredAgentImageGeneration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-no-agent-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	// Provider/config usable, but no agent.image_generation selector.
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: ""})
	tr := newToolWith(t, source, &stubImageProvider{name: "openai", result: pngResult("")}, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want configuration error: %#v", res)
	}
	if !strings.Contains(res.Content, "agent.image_generation") {
		t.Fatalf("error content %q does not mention agent.image_generation", res.Content)
	}
}

func TestExecuteRejectsUnresolvableRef(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-unresolvable"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	// Agent selects an endpoint whose provider has an empty api_key → cannot resolve.
	source := NewSource(newTestImageCfg("", "", ""), fakeAgentSource{ref: "openai:gpt-image"})
	tr := newToolWith(t, source, &stubImageProvider{name: "openai", result: pngResult("")}, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want unresolvable-ref error: %#v", res)
	}
	if !strings.Contains(res.Content, "not a usable image endpoint") {
		t.Fatalf("error content %q does not mention usable endpoint", res.Content)
	}
}

func TestExecuteRejectsMismatchedExplicitModel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-mismatch"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	tr := newToolWith(t, source, &stubImageProvider{name: "openai", result: pngResult("")}, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat","model":"openai:other"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want mismatch error: %#v", res)
	}
	if !strings.Contains(res.Content, "does not match configured agent.image_generation") {
		t.Fatalf("error content %q does not mention mismatch", res.Content)
	}
}

func TestExecuteSuccessWritesFilesAndEmitsDeliverable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-image"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	provider := &stubImageProvider{name: "openai", result: pngResult("small cat")}
	tr := newToolWith(t, source, provider, root)

	events := make(chan types.EngineEvent, 1)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	ctx = tool.WithEventOut(ctx, events)
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat","model":"openai:gpt-image","n":1,"size":"2048x2048","quality":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}

	// Request was handed to the provider with the resolved endpoint + prompt.
	if provider.calls != 1 {
		t.Fatalf("provider.Generate called %d times, want 1", provider.calls)
	}
	if provider.lastReq.Prompt != "small cat" || provider.lastReq.Endpoint.Model != "gpt-image-1" {
		t.Fatalf("unexpected provider request: %#v", provider.lastReq)
	}
	if provider.lastReq.Quality != "high" || provider.lastReq.Size != "2048x2048" {
		t.Fatalf("request hints not forwarded: %#v", provider.lastReq)
	}

	if strings.Contains(res.Content, testPNGB64) {
		t.Fatalf("tool content leaked base64")
	}

	images, ok := res.Metadata["images"].([]GeneratedImage)
	if !ok || len(images) != 1 {
		t.Fatalf("metadata images = %#v", res.Metadata["images"])
	}
	img := images[0]
	if img.MIME != "image/png" || img.Model != "gpt-image-1" || img.Prompt != "small cat" {
		t.Fatalf("unexpected image metadata: %#v", img)
	}
	if res.Metadata["provider"] != "openai" || res.Metadata["endpoint"] != "gpt-image" {
		t.Fatalf("unexpected metadata provider/endpoint: %#v", res.Metadata)
	}
	if !strings.HasPrefix(img.Path, workspace.SessionRoot(root, sid)) {
		t.Fatalf("image path %q is not under session root", img.Path)
	}
	if filepath.Base(filepath.Dir(img.Path)) != generatedDirName {
		t.Fatalf("image path %q not under generated dir", img.Path)
	}
	written, err := os.ReadFile(img.Path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	wantBytes, _ := base64.StdEncoding.DecodeString(testPNGB64)
	if string(written) != string(wantBytes) {
		t.Fatalf("written bytes mismatch")
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

func TestExecuteWritesToTaskDirWhenScopeHasTaskID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-task-scoped"
	tid := "t-abc-123"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	provider := &stubImageProvider{name: "openai", result: pngResult("cat")}
	tr := newToolWith(t, source, provider, root)

	// spawn ctx 携带 TaskID —— content_creator 派活的正常路径
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
	})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"cat","model":"openai:gpt-image","n":1,"size":"2048x2048","quality":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %#v", res)
	}
	images := res.Metadata["images"].([]GeneratedImage)
	got := filepath.Dir(images[0].Path)
	want := workspace.TaskDir(root, sid, tid)
	if got != want {
		t.Fatalf("with TaskID image should land in task_dir:\n got %q\nwant %q", got, want)
	}
	// 确认 generated/ 兜底路径**没**被创建
	if _, err := os.Stat(filepath.Join(workspace.SessionRoot(root, sid), generatedDirName)); err == nil {
		t.Errorf("session-level generated/ should not exist when TaskID is present")
	}
}

func TestExecuteUsesArtifactProducerSessionWhenAgentScopeMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-producer"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	tr := newToolWith(t, source, &stubImageProvider{name: "openai", result: pngResult("")}, root)

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

func TestExecutePermissionDeniedMapsToPermissionError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-perm"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	provider := &stubImageProvider{name: "openai", err: ErrPermissionDeniedf("401 unauthorized")}
	tr := newToolWith(t, source, provider, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want permission error: %#v", res)
	}
	if res.ErrorType != types.ToolErrorPermissionDenied {
		t.Fatalf("error type = %v, want ToolErrorPermissionDenied", res.ErrorType)
	}
	// Permission errors must short-circuit the retry loop.
	if provider.calls != 1 {
		t.Fatalf("provider.Generate called %d times, want 1 (no retry on permission denied)", provider.calls)
	}
}

func TestExecuteTransientErrorRetriesThenFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sid := "sess-transient"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	source := NewSource(newTestImageCfg("sk-x", "https://api.test", "/v1/images/generations"), fakeAgentSource{ref: "openai:gpt-image"})
	provider := &stubImageProvider{name: "openai", err: ErrTransientf("503 unavailable")}
	tr := newToolWith(t, source, provider, root)

	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	res, err := tr.Execute(ctx, json.RawMessage(`{"prompt":"small cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute succeeded, want dependency error: %#v", res)
	}
	if res.ErrorType != types.ToolErrorDependencyFail {
		t.Fatalf("error type = %v, want ToolErrorDependencyFail", res.ErrorType)
	}
	// Initial attempt + 3 backoff retries = 4 calls.
	if provider.calls != 4 {
		t.Fatalf("provider.Generate called %d times, want 4 (1 + 3 retries)", provider.calls)
	}
}
