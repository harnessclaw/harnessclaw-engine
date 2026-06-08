package submittool

import (
	"strings"
	"testing"
)

func TestValidateAgainstSchema_NilSchema(t *testing.T) {
	// No schema means no validation — any value passes (including nil).
	if got := validateAgainstSchema(nil, nil); len(got) != 0 {
		t.Errorf("nil schema should pass any value, got: %v", got)
	}
}

func TestValidateAgainstSchema_RequiredMissing(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"name", "count"},
	}
	value := map[string]any{"name": "x"} // count missing
	fails := validateAgainstSchema(schema, value)
	if len(fails) != 1 || !strings.Contains(fails[0], "count") {
		t.Errorf("expected one failure mentioning count, got: %v", fails)
	}
}

func TestValidateAgainstSchema_NilValueRejected(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"x"}}
	fails := validateAgainstSchema(schema, nil)
	if len(fails) != 1 || !strings.Contains(fails[0], "missing or null") {
		t.Errorf("nil value with non-empty schema should fail, got: %v", fails)
	}
}

func TestValidateAgainstSchema_TypeMismatch(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"age":   map[string]any{"type": "integer"},
			"name":  map[string]any{"type": "string"},
			"price": map[string]any{"type": "number"},
			"on":    map[string]any{"type": "boolean"},
		},
	}
	value := map[string]any{
		"age":   "thirty", // should be integer
		"name":  42,        // should be string
		"price": "free",    // should be number
		"on":    "yes",     // should be boolean
	}
	fails := validateAgainstSchema(schema, value)
	if len(fails) != 4 {
		t.Errorf("want 4 type failures, got %d: %v", len(fails), fails)
	}
}

func TestValidateAgainstSchema_IntegerAcceptsFloatWithZeroFraction(t *testing.T) {
	// JSON numbers come through json.Unmarshal as float64 — an integer
	// schema must still pass when the value is float64(42), not just int.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n": map[string]any{"type": "integer"},
		},
	}
	if fails := validateAgainstSchema(schema, map[string]any{"n": float64(42)}); len(fails) != 0 {
		t.Errorf("integer should accept float64(42), got: %v", fails)
	}
	// But reject 42.5.
	if fails := validateAgainstSchema(schema, map[string]any{"n": 42.5}); len(fails) != 1 {
		t.Errorf("integer should reject 42.5, got: %v", fails)
	}
}

func TestValidateAgainstSchema_Enum(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tone": map[string]any{
				"type": "string",
				"enum": []string{"formal", "casual"},
			},
		},
	}
	if fails := validateAgainstSchema(schema, map[string]any{"tone": "formal"}); len(fails) != 0 {
		t.Errorf("formal should pass, got: %v", fails)
	}
	fails := validateAgainstSchema(schema, map[string]any{"tone": "snarky"})
	if len(fails) != 1 || !strings.Contains(fails[0], "enum") {
		t.Errorf("snarky should fail with enum reason, got: %v", fails)
	}
}

func TestValidateAgainstSchema_Minimum(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"word_count": map[string]any{
				"type":    "integer",
				"minimum": 1,
			},
		},
	}
	if fails := validateAgainstSchema(schema, map[string]any{"word_count": float64(0)}); len(fails) != 1 {
		t.Errorf("0 should fail minimum=1, got: %v", fails)
	}
	if fails := validateAgainstSchema(schema, map[string]any{"word_count": float64(50)}); len(fails) != 0 {
		t.Errorf("50 should pass minimum=1, got: %v", fails)
	}
}

func TestValidateAgainstSchema_WriterRealSchema(t *testing.T) {
	// The actual writer OutputSchema — make sure a realistic submission passes.
	schema := map[string]any{
		"type":     "object",
		"required": []string{"artifact_role", "format", "word_count", "tone"},
		"properties": map[string]any{
			"artifact_role": map[string]any{
				"type": "string",
				"enum": []string{"draft", "final", "revision"},
			},
			"format": map[string]any{
				"type": "string",
				"enum": []string{"markdown", "plaintext", "html"},
			},
			"word_count": map[string]any{"type": "integer", "minimum": 1},
			"tone": map[string]any{
				"type": "string",
				"enum": []string{"formal", "professional", "friendly", "casual", "neutral"},
			},
			"language": map[string]any{"type": "string"},
		},
	}
	good := map[string]any{
		"artifact_role": "draft",
		"format":        "markdown",
		"word_count":    float64(220),
		"tone":          "formal",
		"language":      "zh",
	}
	if fails := validateAgainstSchema(schema, good); len(fails) != 0 {
		t.Errorf("realistic writer submission should pass, got: %v", fails)
	}
	bad := map[string]any{
		"artifact_role": "draft",
		"format":        "pdf", // not in enum
		"word_count":    float64(220),
		"tone":          "formal",
	}
	fails := validateAgainstSchema(schema, bad)
	if len(fails) != 1 || !strings.Contains(fails[0], "format") {
		t.Errorf("pdf format should be rejected, got: %v", fails)
	}
}
