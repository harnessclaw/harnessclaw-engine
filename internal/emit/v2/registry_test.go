package emitv2

import (
	"testing"
	"time"
)

func TestLookupCardMeta_KnownKinds(t *testing.T) {
	cases := []struct {
		kind         CardKind
		wantTracked  bool
		wantTimeout  time.Duration
		wantRoleNotEmpty bool
	}{
		{CardTurn, true, 600_000 * time.Millisecond, true},
		{CardStep, true, 60_000 * time.Millisecond, true},
		{CardTool, true, 120_000 * time.Millisecond, true},
		{CardArtifact, false, 0, true},
		{CardTodo, false, 0, true},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			m := LookupCardMeta(c.kind)
			if c.wantRoleNotEmpty && m.DefaultRole == "" {
				t.Errorf("DefaultRole empty for %s", c.kind)
			}
			if c.wantTracked && m.Lifecycle != LifecycleTracked {
				t.Errorf("expected %s tracked, got %s", c.kind, m.Lifecycle)
			}
			if !c.wantTracked && m.Lifecycle == LifecycleTracked {
				t.Errorf("expected %s untracked", c.kind)
			}
			to := OrphanTimeout(c.kind)
			if to != c.wantTimeout {
				t.Errorf("OrphanTimeout(%s) = %v, want %v", c.kind, to, c.wantTimeout)
			}
		})
	}
}

func TestLookupCardMeta_UnknownKind(t *testing.T) {
	m := LookupCardMeta(CardKind("unknown_kind"))
	// Unknown kinds must fall back permissively.
	if m.Lifecycle == LifecycleTracked {
		t.Error("unknown kind should NOT be tracked")
	}
	if m.DefaultIcon == "" {
		t.Error("unknown kind should have a fallback icon")
	}
}

func TestLookupErrorMeta_KnownTypes(t *testing.T) {
	for _, typ := range []ErrorType{
		ErrorTypeToolTimeout, ErrorTypeOrphanTimeout, ErrorTypeRateLimit,
		ErrorTypeOverloaded, ErrorTypeContractFail, ErrorTypeDependencyFail,
		ErrorTypeUserAborted, ErrorTypePermissionDenied, ErrorTypeMaxTurns,
		ErrorTypeContextExceeded, ErrorTypeModelError, ErrorTypeBudgetExhausted,
		ErrorTypeInvalidInput, ErrorTypeInternal,
	} {
		t.Run(string(typ), func(t *testing.T) {
			m := LookupErrorMeta(typ)
			if m.DefaultUserMessage == "" {
				t.Errorf("ErrorType %s missing DefaultUserMessage", typ)
			}
		})
	}
}

func TestLookupErrorMeta_UnknownFallsBackToInternal(t *testing.T) {
	m := LookupErrorMeta(ErrorType("ufo_error"))
	internal := errorTypeMeta[ErrorTypeInternal]
	if m.DefaultUserMessage != internal.DefaultUserMessage {
		t.Errorf("unknown ErrorType should fall back to Internal; got %q", m.DefaultUserMessage)
	}
}

func TestNewError_PopulatesRegistryDefaults(t *testing.T) {
	e := NewError(ErrorTypeToolTimeout, "Bash exceeded 120s")
	if e.UserMessage == "" {
		t.Error("NewError should populate UserMessage from registry")
	}
	if !e.Retryable {
		t.Error("ErrorTypeToolTimeout should be Retryable by default")
	}
	if e.Type != ErrorTypeToolTimeout {
		t.Errorf("Type = %s, want %s", e.Type, ErrorTypeToolTimeout)
	}
	if e.Message != "Bash exceeded 120s" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestErrorInfo_Builders(t *testing.T) {
	e := NewError(ErrorTypeOrphanTimeout, "x").
		WithUserMessage("我等不到结果了").
		WithCode("STEP_DEAD").
		WithRetryable(true).
		WithRetryAfter(5000).
		WithRecovery("retry", "step_b")

	if e.UserMessage != "我等不到结果了" {
		t.Error("WithUserMessage")
	}
	if e.Code != "STEP_DEAD" {
		t.Error("WithCode")
	}
	if !e.Retryable {
		t.Error("WithRetryable should override default false")
	}
	if e.RetryAfterMs != 5000 {
		t.Error("WithRetryAfter")
	}
	if e.Recovery == nil || e.Recovery.Action != "retry" || e.Recovery.NextCardID != "step_b" {
		t.Errorf("WithRecovery = %+v", e.Recovery)
	}
}

func TestSeverityForClose(t *testing.T) {
	cases := []struct {
		status Status
		want   Severity
	}{
		{StatusOK, SeverityInfo},
		{StatusFailed, SeverityError},
		{StatusSkipped, SeverityWarn},
		{StatusCancelled, SeverityWarn},
	}
	for _, c := range cases {
		if got := SeverityForClose(c.status); got != c.want {
			t.Errorf("SeverityForClose(%s) = %s, want %s", c.status, got, c.want)
		}
	}
}
