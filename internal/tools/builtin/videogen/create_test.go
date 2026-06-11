package videogen

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
