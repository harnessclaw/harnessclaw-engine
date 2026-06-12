package videogen

import (
	"context"
	"testing"
	"time"
)

type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) SubmitTask(context.Context, SubmitRequest) (*SubmitResult, error) {
	return &SubmitResult{TaskID: "t", SubmittedAt: time.Unix(0, 0)}, nil
}
func (f *fakeProvider) QueryTask(context.Context, QueryRequest) (*QueryResult, error) {
	return &QueryResult{Status: StatusQueued}, nil
}
func (f *fakeProvider) DownloadVideo(context.Context, string) ([]byte, string, error) {
	return []byte("x"), "video/mp4", nil
}

func TestRegistryRegisterGetList(t *testing.T) {
	t.Parallel()
	r := NewProviderRegistry()
	if err := r.Register(&fakeProvider{name: "doubao"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(&fakeProvider{name: "doubao"}); err == nil {
		t.Fatal("duplicate register must error")
	}
	p, ok := r.Get("doubao")
	if !ok || p.Name() != "doubao" {
		t.Fatalf("Get returned %v ok=%v", p, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get of unknown must be ok=false")
	}
	if got := r.List(); len(got) != 1 || got[0] != "doubao" {
		t.Fatalf("List = %v", got)
	}
}
