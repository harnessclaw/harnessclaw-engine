package registry

import (
	"reflect"
	"sort"
	"testing"
)

func TestDeriveCapabilities_VisionGoesMultimodal(t *testing.T) {
	got := DeriveCapabilities(SupportsFlags{Vision: true})
	sort.Strings(got)
	want := []string{"multimodal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDeriveCapabilities_AllBuckets(t *testing.T) {
	got := DeriveCapabilities(SupportsFlags{
		PDFInput:        true,
		ImageGeneration: true,
		FunctionCalling: true,
		Reasoning:       true,
		WebSearch:       true,
	})
	sort.Strings(got)
	want := []string{"image_generation", "multimodal", "reasoning", "search", "tools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDeriveCapabilities_TextOnlyEmpty(t *testing.T) {
	got := DeriveCapabilities(SupportsFlags{Streaming: true})
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestAcceptsModality_VisionFalseRejectsImage(t *testing.T) {
	if AcceptsModality(SupportsFlags{}, "image") {
		t.Error("vision=false must reject image")
	}
	if !AcceptsModality(SupportsFlags{Vision: true}, "image") {
		t.Error("vision=true must accept image")
	}
}

func TestAcceptsModality_PdfMapsToPDFInput(t *testing.T) {
	if AcceptsModality(SupportsFlags{Vision: true}, "pdf") {
		t.Error("pdf must require PDFInput, not Vision")
	}
	if !AcceptsModality(SupportsFlags{PDFInput: true}, "pdf") {
		t.Error("PDFInput=true must accept pdf")
	}
}

func TestAcceptsModality_UnknownReturnsFalse(t *testing.T) {
	// Defense-in-depth: a typo on the wire shouldn't bypass the gate.
	if AcceptsModality(SupportsFlags{Vision: true, PDFInput: true, AudioInput: true, VideoInput: true}, "hologram") {
		t.Error("unknown modality must fail closed")
	}
}

func TestSupportsFromTokens_AllKnownTokens(t *testing.T) {
	got := SupportsFromTokens([]string{"vision", "pdf", "audio", "video", "image_generation", "reasoning", "tools", "search"})
	if !got.Vision || !got.PDFInput || !got.AudioInput || !got.VideoInput {
		t.Error("multimodal flags missing")
	}
	if !got.ImageGeneration {
		t.Error("image generation flag missing")
	}
	if !got.Reasoning || !got.FunctionCalling || !got.WebSearch {
		t.Error("capability flags missing")
	}
}

func TestSupportsFromTokens_UnknownSilentlyDropped(t *testing.T) {
	got := SupportsFromTokens([]string{"vision", "rainbow"})
	if !got.Vision {
		t.Error("vision should be set")
	}
	// rainbow has no mapping — function ignores it; no panic
}

func TestMergeOverride_PreservesOperationalFlags(t *testing.T) {
	base := SupportsFlags{
		Vision:         true, // capability flag — will be overridden
		Reasoning:      true, // capability flag — will be overridden
		Streaming:      true, // operational — must survive
		PromptCaching:  true, // operational — must survive
		SystemMessages: true, // operational — must survive
	}
	override := SupportsFromTokens([]string{"tools"}) // no vision, no reasoning
	got := MergeOverride(base, override)

	if got.Vision {
		t.Error("override should clear unset capability (Vision)")
	}
	if got.Reasoning {
		t.Error("override should clear unset capability (Reasoning)")
	}
	if !got.FunctionCalling {
		t.Error("override should set listed capability (tools→FunctionCalling)")
	}
	if got.ImageGeneration {
		t.Error("override should clear unset capability (ImageGeneration)")
	}
	if !got.Streaming || !got.PromptCaching || !got.SystemMessages {
		t.Errorf("operational flags must survive override: %+v", got)
	}
}

func TestFilterKnownTokens_DropsUnknown(t *testing.T) {
	k, u := FilterKnownTokens([]string{"vision", "rainbow", "tools", "", "pdf"})
	if len(k) != 3 || k[0] != "vision" || k[1] != "tools" || k[2] != "pdf" {
		t.Errorf("known: %v", k)
	}
	if len(u) != 1 || u[0] != "rainbow" {
		t.Errorf("unknown: %v", u)
	}
}

func TestFilterKnownTokens_EmptyInput(t *testing.T) {
	k, u := FilterKnownTokens(nil)
	if k != nil || u != nil {
		t.Errorf("nil input should return (nil, nil), got (%v, %v)", k, u)
	}
}
