package engine

import (
	"encoding/json"
	"testing"

	"harnessclaw-go/internal/engine/toolexec"
)

// TestStripIntent_ParsesObjectAndRemovesIntent guards the executor's
// extract/strip step. ToolPool.Schemas() forces every tool input to carry
// an `intent` field; the executor must lift it into a progress event AND
// remove it before invoking the tool — otherwise the tool's own validator
// would either reject the unexpected field or accidentally consume it.
func TestStripIntent_ParsesObjectAndRemovesIntent(t *testing.T) {
	cleaned, intent := toolexec.StripIntent(`{"file_path":"/x","intent":"读取入口文件 main.go"}`)
	if intent != "读取入口文件 main.go" {
		t.Errorf("intent: got %q", intent)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cleaned), &m); err != nil {
		t.Fatalf("cleaned input not valid JSON: %v\n%s", err, cleaned)
	}
	if _, has := m["intent"]; has {
		t.Errorf("intent leaked into cleaned input: %s", cleaned)
	}
	if m["file_path"] != "/x" {
		t.Errorf("non-intent fields lost: %v", m)
	}
}

// TestStripIntent_TolerantOfMissingIntent — if the model didn't fill
// intent (provider relaxed validation), we degrade silently rather than
// crash. The tool still runs and the user just doesn't see a progress
// sentence for that call.
func TestStripIntent_TolerantOfMissingIntent(t *testing.T) {
	cleaned, intent := toolexec.StripIntent(`{"file_path":"/x"}`)
	if intent != "" {
		t.Errorf("intent should be empty, got %q", intent)
	}
	if cleaned != `{"file_path":"/x"}` {
		t.Errorf("input should be unchanged, got %s", cleaned)
	}
}

// TestStripIntent_NotAnObject — array/scalar inputs aren't valid for
// schema-validated tools, but we must not crash even if a malformed call
// reaches us. Pass through, no progress event.
func TestStripIntent_NotAnObject(t *testing.T) {
	cleaned, intent := toolexec.StripIntent(`["not", "an", "object"]`)
	if intent != "" || cleaned != `["not", "an", "object"]` {
		t.Errorf("array input mishandled: cleaned=%q intent=%q", cleaned, intent)
	}
}

