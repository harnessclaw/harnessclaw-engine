package websocket

import (
	"errors"
	"reflect"
	"testing"

	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/pkg/types"
)

// TestTranslator_UnsupportedModalityFrame is the wire-shape contract for
// the multimodal gate rejection path: the router emits an
// EngineEventError with Terminal.Reason=unsupported_modality and the
// rich ErrorDetails map; the translator must turn that into a
// session.event{kind:error} carrying error.type=unsupported_modality
// plus the user-facing message and rejected-modality list under details.
func TestTranslator_UnsupportedModalityFrame(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_um")
	tr := NewTranslator()

	tr.Translate(em, "sess_um", &types.EngineEvent{
		Type:  types.EngineEventError,
		Error: errors.New("model anthropic:claude-haiku-4-5 does not support modalities [image]"),
		Terminal: &types.Terminal{
			Reason:  types.TerminalUnsupportedModality,
			Message: "model x does not support modalities [image]",
		},
		ErrorDetails: map[string]any{
			"model":               "anthropic:claude-haiku-4-5",
			"rejected_modalities": []string{"image"},
			"user_message":        "当前模型不支持图片输入，请切换到具备多模态能力的模型后重试。",
			"error_code":          "model_lacks_modality",
		},
	})

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("want 1 frame, got %d", len(events))
	}
	if events[0].Type != emitv2.EventSession {
		t.Fatalf("frame type: %s", events[0].Type)
	}
	// session.event payload is a SessionPayload{Kind, Inner}; Inner is
	// the map[string]any{"error": ...} we passed in.
	sp, ok := events[0].Payload.(emitv2.SessionPayload)
	if !ok {
		t.Fatalf("payload not SessionPayload: %T", events[0].Payload)
	}
	body, ok := sp.Inner.(map[string]any)
	if !ok {
		t.Fatalf("inner not a map: %T", sp.Inner)
	}
	errInfo, ok := body["error"].(*emitv2.ErrorInfo)
	if !ok {
		t.Fatalf("error key not ErrorInfo: %T", body["error"])
	}
	if errInfo.Type != emitv2.ErrorTypeUnsupportedModality {
		t.Errorf("error.type: got %s want unsupported_modality", errInfo.Type)
	}
	if errInfo.UserMessage == "" || errInfo.UserMessage == "出了点意外情况" {
		t.Errorf("user_message must carry the gate's Chinese copy: %q", errInfo.UserMessage)
	}
	if errInfo.Code != "model_lacks_modality" {
		t.Errorf("code: %q", errInfo.Code)
	}
	if errInfo.Retryable {
		t.Error("unsupported_modality must NOT be retryable (registry default)")
	}
	// Details map carries model + rejected_modalities; user_message and
	// error_code were lifted to typed fields so they shouldn't be
	// duplicated under details.
	if model, _ := errInfo.Details["model"].(string); model != "anthropic:claude-haiku-4-5" {
		t.Errorf("details.model: %q", model)
	}
	rm, _ := errInfo.Details["rejected_modalities"].([]string)
	if !reflect.DeepEqual(rm, []string{"image"}) {
		t.Errorf("details.rejected_modalities: %v", rm)
	}
	if _, dup := errInfo.Details["user_message"]; dup {
		t.Error("user_message should be promoted out of details, not duplicated")
	}
	if _, dup := errInfo.Details["error_code"]; dup {
		t.Error("error_code should be promoted out of details, not duplicated")
	}
}

// TestTranslator_GenericErrorFallsBackToInternal verifies the pre-existing
// behavior is preserved: an error event without Terminal info still
// becomes ErrorTypeInternal with the default user message.
func TestTranslator_GenericErrorFallsBackToInternal(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_g")
	tr := NewTranslator()

	tr.Translate(em, "sess_g", &types.EngineEvent{
		Type:  types.EngineEventError,
		Error: errors.New("boom"),
	})

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("want 1 frame, got %d", len(events))
	}
	sp := events[0].Payload.(emitv2.SessionPayload)
	body := sp.Inner.(map[string]any)
	errInfo := body["error"].(*emitv2.ErrorInfo)
	if errInfo.Type != emitv2.ErrorTypeInternal {
		t.Errorf("type: %s want internal", errInfo.Type)
	}
	if errInfo.Message != "boom" {
		t.Errorf("message: %q", errInfo.Message)
	}
}
