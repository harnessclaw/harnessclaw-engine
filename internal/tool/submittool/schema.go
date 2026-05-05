package submittool

import (
	"fmt"
	"strings"
)

// validateAgainstSchema checks `value` against a minimal subset of JSON
// Schema sufficient for AgentDefinition.OutputSchema use. Returns a list
// of human-readable failure reasons; empty slice means "passed".
//
// Why a minimal in-house validator instead of pulling in a real JSON
// Schema lib (e.g. santhosh-tekuri/jsonschema): every existing
// AgentDefinition uses only the four primitives below — type / required
// / enum / minimum — so one focused 80-line function buys us schema
// enforcement without dragging in 5K LOC and a transitive dep tree we
// have to audit. If schemas grow more complex we revisit.
//
// Supported keywords:
//   - top-level "type" must be "object"; root value must be a map[string]any.
//   - "required": list of property names that must exist (and be non-nil).
//   - "properties": per-key recursive validation of:
//       * "type": one of "string" / "integer" / "number" / "boolean".
//       * "enum": list of literal values; the field must equal one.
//       * "minimum": numeric lower bound (integer / number only).
//
// Unsupported keywords are silently ignored — the worst case is a less
// strict check, never a false positive. Anything that can't be validated
// in-house can also be enforced in the tool's hardcoded InputSchema if
// needed.
// ValidateAgainstSchema is the exported form of validateAgainstSchema.
// Engine-layer callers (e.g., SpawnSync InputSchema validation) use this
// so the same logic covers both input and output contracts without copying.
func ValidateAgainstSchema(schema, value map[string]any) []string {
	return validateAgainstSchema(schema, value)
}

func validateAgainstSchema(schema, value map[string]any) []string {
	if len(schema) == 0 {
		return nil
	}
	rootType, _ := schema["type"].(string)
	if rootType != "" && rootType != "object" {
		return []string{fmt.Sprintf("schema root type %q unsupported (only \"object\" handled)", rootType)}
	}
	if value == nil {
		return []string{"result is required when output_schema is set, but it was missing or null"}
	}

	var fails []string

	// Required keys.
	for _, key := range toStringSlice(schema["required"]) {
		v, present := value[key]
		if !present || v == nil {
			fails = append(fails, fmt.Sprintf("required field %q is missing", key))
		}
	}

	// Per-property checks.
	props, _ := schema["properties"].(map[string]any)
	for propName, propSpecAny := range props {
		propSpec, ok := propSpecAny.(map[string]any)
		if !ok {
			continue
		}
		propVal, present := value[propName]
		if !present || propVal == nil {
			// Required-ness was handled above; if not required, skip.
			continue
		}
		fails = append(fails, validateProp(propName, propSpec, propVal)...)
	}

	return fails
}

func validateProp(name string, spec map[string]any, val any) []string {
	var fails []string
	wantType, _ := spec["type"].(string)
	switch wantType {
	case "string":
		if _, ok := val.(string); !ok {
			return []string{fmt.Sprintf("field %q must be string, got %T", name, val)}
		}
	case "integer":
		// JSON numbers come through as float64 from json.Unmarshal; accept
		// values whose fractional part is zero.
		f, ok := toFloat(val)
		if !ok {
			return []string{fmt.Sprintf("field %q must be integer, got %T", name, val)}
		}
		if f != float64(int64(f)) {
			return []string{fmt.Sprintf("field %q must be integer, got %v", name, val)}
		}
	case "number":
		if _, ok := toFloat(val); !ok {
			return []string{fmt.Sprintf("field %q must be number, got %T", name, val)}
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return []string{fmt.Sprintf("field %q must be boolean, got %T", name, val)}
		}
	}

	// enum: literal-equality membership test.
	if enum, ok := spec["enum"]; ok {
		enumList := toAnySlice(enum)
		matched := false
		for _, allowed := range enumList {
			if equalLiteral(allowed, val) {
				matched = true
				break
			}
		}
		if !matched {
			fails = append(fails, fmt.Sprintf("field %q value %v not in enum %v",
				name, val, formatEnum(enumList)))
		}
	}

	// minimum: numeric lower bound.
	if min, ok := spec["minimum"]; ok {
		minF, okMin := toFloat(min)
		valF, okVal := toFloat(val)
		if okMin && okVal && valF < minF {
			fails = append(fails, fmt.Sprintf("field %q value %v is below minimum %v",
				name, val, min))
		}
	}

	return fails
}

func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		out := make([]string, len(s))
		copy(out, s)
		return out
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func toAnySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	if s, ok := v.([]string); ok {
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func equalLiteral(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		af, okA := toFloat(a)
		bf, okB := toFloat(b)
		return okA && okB && af == bf
	}
}

func formatEnum(items []any) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%v", it))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
