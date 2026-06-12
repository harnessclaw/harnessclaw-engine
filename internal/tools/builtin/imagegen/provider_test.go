package imagegen

import (
	"context"
	"errors"
	"testing"
)

type fakeImgProvider struct{ name string }

func (f *fakeImgProvider) Name() string { return f.name }
func (f *fakeImgProvider) Generate(context.Context, GenerateRequest) (*GenerateResult, error) {
	return &GenerateResult{Images: []GeneratedImageData{{B64JSON: "x", MIME: "image/png"}}}, nil
}

func TestImageSentinelErrors(t *testing.T) {
	t.Parallel()
	pd := ErrPermissionDeniedf("openai: 401")
	if !errors.Is(pd, ErrPermissionDenied) {
		t.Fatal("must match ErrPermissionDenied")
	}
	if errors.Is(pd, ErrTransient) {
		t.Fatal("must not match ErrTransient")
	}
}

func TestImageRegistry(t *testing.T) {
	t.Parallel()
	r := NewProviderRegistry()
	if err := r.Register(&fakeImgProvider{name: "openai"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&fakeImgProvider{name: "openai"}); err == nil {
		t.Fatal("duplicate must error")
	}
	if _, ok := r.Get("openai"); !ok {
		t.Fatal("Get openai")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get missing must be false")
	}
	if got := r.List(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("List = %v", got)
	}
}
