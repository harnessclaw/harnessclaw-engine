package tool

import (
	"reflect"
	"sort"
	"testing"
)

// TestWithIntentField_NilSchema covers the worst case: a Tool returned a
// nil InputSchema. We can't enforce intent on a tool that has no schema,
// but we must produce a valid object schema with intent required so the
// model still gets prompted to fill it.
func TestWithIntentField_NilSchema(t *testing.T) {
	got := WithIntentField(nil)
	if got["type"] != "object" {
		t.Errorf("type: got %v, want object", got["type"])
	}
	props, _ := got["properties"].(map[string]any)
	if _, ok := props[IntentFieldName]; !ok {
		t.Error("intent property missing")
	}
	required := got["required"].([]any)
	if len(required) != 1 || required[0] != IntentFieldName {
		t.Errorf("required: got %v, want [intent]", required)
	}
}

// TestWithIntentField_MergesIntoExistingSchema is the common case — a tool
// already has properties + required, we add intent without losing anything.
func TestWithIntentField_MergesIntoExistingSchema(t *testing.T) {
	orig := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
		},
		"required": []any{"file_path", "content"},
	}
	got := WithIntentField(orig)

	props := got["properties"].(map[string]any)
	for _, key := range []string{"file_path", "content", IntentFieldName} {
		if _, ok := props[key]; !ok {
			t.Errorf("property %q missing", key)
		}
	}
	required := got["required"].([]any)
	asStrings := make([]string, len(required))
	for i, r := range required {
		asStrings[i] = r.(string)
	}
	sort.Strings(asStrings)
	want := []string{"content", "file_path", IntentFieldName}
	if !reflect.DeepEqual(asStrings, want) {
		t.Errorf("required: got %v, want %v", asStrings, want)
	}
}

// TestWithIntentField_DoesNotMutateOriginal guards against subtle bugs where
// repeated Schemas() calls would double-append `intent` or leak across tools.
func TestWithIntentField_DoesNotMutateOriginal(t *testing.T) {
	orig := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
		},
		"required": []any{"x"},
	}
	_ = WithIntentField(orig)

	props := orig["properties"].(map[string]any)
	if _, ok := props[IntentFieldName]; ok {
		t.Error("original properties were mutated — Schemas would corrupt the tool's own schema across calls")
	}
	required := orig["required"].([]any)
	if len(required) != 1 {
		t.Errorf("original required mutated: got %v", required)
	}
}

// TestWithIntentField_RespectsExistingIntent — if a tool author already
// declares an `intent` property with their own meaning, we don't override
// it. This keeps the door open for tools that genuinely need a custom
// intent semantics (none today, but a sane future-proof default).
func TestWithIntentField_RespectsExistingIntent(t *testing.T) {
	orig := map[string]any{
		"type": "object",
		"properties": map[string]any{
			IntentFieldName: map[string]any{
				"type":        "string",
				"description": "tool's own meaning",
				"enum":        []any{"create", "update"},
			},
		},
		"required": []any{IntentFieldName},
	}
	got := WithIntentField(orig)
	props := got["properties"].(map[string]any)
	intent := props[IntentFieldName].(map[string]any)
	if intent["description"] != "tool's own meaning" {
		t.Errorf("framework overrode tool's own intent description: %v", intent)
	}
	if _, ok := intent["enum"]; !ok {
		t.Error("framework dropped tool's intent enum constraint")
	}
}

// TestWithIntentField_AcceptsStringSliceRequired — Go tools sometimes return
// required as []string (more natural in Go) instead of []any. We must accept
// both since the input is from arbitrary tool authors.
func TestWithIntentField_AcceptsStringSliceRequired(t *testing.T) {
	orig := map[string]any{
		"type":     "object",
		"required": []string{"foo"},
	}
	got := WithIntentField(orig)
	required := got["required"].([]any)
	if len(required) != 2 {
		t.Fatalf("required: got %v, want [foo, intent]", required)
	}
}
