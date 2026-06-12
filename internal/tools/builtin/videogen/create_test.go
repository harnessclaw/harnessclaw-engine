package videogen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
	"go.uber.org/zap"
)

type stubProvider struct {
	name       string
	submitFn   func(SubmitRequest) (*SubmitResult, error)
	queryFn    func(QueryRequest) (*QueryResult, error)
	downloadFn func(string) ([]byte, string, error)
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) SubmitTask(_ context.Context, r SubmitRequest) (*SubmitResult, error) {
	return s.submitFn(r)
}
func (s *stubProvider) QueryTask(_ context.Context, r QueryRequest) (*QueryResult, error) {
	return s.queryFn(r)
}
func (s *stubProvider) DownloadVideo(_ context.Context, url string) ([]byte, string, error) {
	return s.downloadFn(url)
}

func newCreateFixture(t *testing.T, submit func(SubmitRequest) (*SubmitResult, error)) *VideoCreateTool {
	t.Helper()
	reg := NewProviderRegistry()
	if err := reg.Register(&stubProvider{name: "doubao", submitFn: submit}); err != nil {
		t.Fatal(err)
	}
	src := NewSource(newTestVideoCfg("sk-x", "https://base"), fakeAgentSource{ref: "doubao:seedance-lite-i2v"})
	return NewCreate(src, reg, t.TempDir(), zap.NewNop())
}

func TestCreateValidateInput(t *testing.T) {
	t.Parallel()
	tr := newCreateFixture(t, nil)
	if err := tr.ValidateInput(json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing prompt must error")
	}
	if err := tr.ValidateInput(json.RawMessage(`{"prompt":"hi","aspect_ratio":"weird"}`)); err == nil {
		t.Fatal("invalid aspect_ratio must error")
	}
	if err := tr.ValidateInput(json.RawMessage(`{"prompt":"hi","duration_s":7}`)); err == nil {
		t.Fatal("invalid duration must error")
	}
	if err := tr.ValidateInput(json.RawMessage(`{"prompt":"hi"}`)); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestCreateIsEnabled(t *testing.T) {
	t.Parallel()
	tr := newCreateFixture(t, nil)
	if !tr.IsEnabled() {
		t.Fatal("should be enabled when endpoint resolves + provider registered")
	}
	reg := NewProviderRegistry()
	_ = reg.Register(&stubProvider{name: "doubao"})
	src := NewSource(newTestVideoCfg("sk-x", ""), fakeAgentSource{ref: ""})
	if NewCreate(src, reg, t.TempDir(), zap.NewNop()).IsEnabled() {
		t.Fatal("empty selector -> disabled")
	}
}

func TestCreateExecuteSuccess(t *testing.T) {
	t.Parallel()
	submittedAt := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	var gotReq SubmitRequest
	tr := newCreateFixture(t, func(r SubmitRequest) (*SubmitResult, error) {
		gotReq = r
		return &SubmitResult{TaskID: "cgt-123", SubmittedAt: submittedAt}, nil
	})
	res, err := tr.Execute(context.Background(), json.RawMessage(`{"prompt":"a cat","duration_s":10,"aspect_ratio":"9:16"}`))
	if err != nil {
		t.Fatalf("Execute returned go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}
	if gotReq.Endpoint.Model != "doubao-seedance-1-0-lite-i2v-250428" || gotReq.Endpoint.APIKey != "sk-x" {
		t.Fatalf("provider got wrong endpoint: %+v", gotReq.Endpoint)
	}
	if gotReq.DurationS != 10 || gotReq.AspectRatio != "9:16" || gotReq.Prompt != "a cat" {
		t.Fatalf("provider got wrong params: %+v", gotReq)
	}
	if res.Metadata["task_id"] != "cgt-123" {
		t.Fatalf("metadata task_id = %v", res.Metadata["task_id"])
	}
}

// writeTestPNG writes a minimal PNG-signature file: enough bytes for
// http.DetectContentType to classify it as image/png.
func writeTestPNG(t *testing.T, dir string) string {
	t.Helper()
	data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	path := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCreateExecuteImagePath: image_path makes the tool read the local
// file itself and synthesize a base64 data URI for the provider.
func TestCreateExecuteImagePath(t *testing.T) {
	t.Parallel()
	var gotReq SubmitRequest
	tr := newCreateFixture(t, func(r SubmitRequest) (*SubmitResult, error) {
		gotReq = r
		return &SubmitResult{TaskID: "cgt-img", SubmittedAt: time.Now()}, nil
	})
	path := writeTestPNG(t, t.TempDir())
	raw := json.RawMessage(fmt.Sprintf(`{"prompt":"x","image_path":%q}`, path))
	res, err := tr.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}
	if !strings.HasPrefix(gotReq.ImageB64, "data:image/png;base64,") {
		t.Fatalf("provider must receive a png data URI, got prefix: %.40q", gotReq.ImageB64)
	}
}

// TestCreateImagePathScopeRejected: an AgentScope on ctx restricts
// image_path to SessionRoot/ReadScope — outside paths are a validation
// error, not a file read.
func TestCreateImagePathScopeRejected(t *testing.T) {
	t.Parallel()
	tr := newCreateFixture(t, func(SubmitRequest) (*SubmitResult, error) {
		t.Fatal("provider must not be called when scope rejects")
		return nil, nil
	})
	path := writeTestPNG(t, t.TempDir()) // outside the scope's SessionRoot
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: t.TempDir()})
	raw := json.RawMessage(fmt.Sprintf(`{"prompt":"x","image_path":%q}`, path))
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned go error: %v", err)
	}
	if !res.IsError || res.ErrorType != types.ToolErrorInvalidInput {
		t.Fatalf("expected invalid_input error for out-of-scope path, got %+v", res)
	}
	if !strings.Contains(res.Content, "outside allowed scope") {
		t.Errorf("error must mention scope: %s", res.Content)
	}
}

// TestCreateImagePathInScopeAllowed: a path under SessionRoot passes
// the scope check and reaches the provider as a data URI.
func TestCreateImagePathInScopeAllowed(t *testing.T) {
	t.Parallel()
	var gotReq SubmitRequest
	tr := newCreateFixture(t, func(r SubmitRequest) (*SubmitResult, error) {
		gotReq = r
		return &SubmitResult{TaskID: "cgt-scoped", SubmittedAt: time.Now()}, nil
	})
	root := t.TempDir()
	path := writeTestPNG(t, root)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: root})
	raw := json.RawMessage(fmt.Sprintf(`{"prompt":"x","image_path":%q}`, path))
	res, err := tr.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("in-scope path must be allowed: %s", res.Content)
	}
	if !strings.HasPrefix(gotReq.ImageB64, "data:image/png;base64,") {
		t.Fatalf("provider must receive a png data URI, got prefix: %.40q", gotReq.ImageB64)
	}
}

func TestCreateExecutePermissionDenied(t *testing.T) {
	t.Parallel()
	tr := newCreateFixture(t, func(SubmitRequest) (*SubmitResult, error) {
		return nil, wrapErr(ErrPermissionDenied, "doubao: 401 bad key")
	})
	res, _ := tr.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if !res.IsError || res.ErrorType != types.ToolErrorPermissionDenied {
		t.Fatalf("expected permission_denied error, got %+v", res)
	}
}
