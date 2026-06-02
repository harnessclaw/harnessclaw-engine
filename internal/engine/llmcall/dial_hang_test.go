package llmcall

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// chatDialHangProvider is the regression mock for the "SDK ignores ctx
// during dial" pathology. Chat() never returns and DOES NOT poll ctx —
// modelling bifrost-anthropic getting wedged in net.Dial / TLS
// handshake against a black-holed gateway, where the cancel signal
// from our first-byte watchdog has no effect because the runtime path
// inside the SDK doesn't honour ctx until after the connection is up.
//
// Before the goroutine-isolation fix, CallLLMOnce would park here for
// the full LLMAPITimeout (10 min) instead of the FirstByte budget
// (~2 min in production). After the fix, the watchdog's callCancel
// must let the main goroutine return immediately even though Chat()
// itself is still stuck.
type chatDialHangProvider struct{}

func (p *chatDialHangProvider) Name() string { return "dial-hang-mock" }

func (p *chatDialHangProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

// Provider interface compliance — Chat blocks forever, ignores ctx.
func (p *chatDialHangProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	// Block the caller's goroutine forever. Intentionally do NOT
	// select on ctx.Done() — this is the whole point of the
	// regression mock: the upstream SDK isn't honouring the cancel.
	select {}
}

func TestCallLLMOnce_DialHangFiresFirstByteWatchdog(t *testing.T) {
	prov := &chatDialHangProvider{}
	timeouts := LLMCallTimeouts{FirstByte: 50 * time.Millisecond}

	start := time.Now()
	result := CallLLMOnce(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		nil, // out
		nil, // planningOut
		timeouts,
		zap.NewNop(),
		"agent-test",
	)
	elapsed := time.Since(start)

	if result.StreamErr == nil {
		t.Fatal("expected first-byte watchdog to surface an error when Chat() hangs in dial")
	}
	if !errors.Is(result.StreamErr, errFirstByteTimeout) {
		t.Errorf("error should wrap errFirstByteTimeout (since the watchdog fired); got %v", result.StreamErr)
	}
	// Budget is 50ms. Pre-fix the function would have parked on
	// Chat() forever (test would time out at the go test default
	// 10min). 500ms is generous headroom for goroutine scheduling.
	if elapsed > 500*time.Millisecond {
		t.Errorf("CallLLMOnce returned after %v; expected <500ms (watchdog should bypass hung Chat)", elapsed)
	}
}

func TestCallLLMOnce_DialHangRespectsAPITimeoutWhenNoFirstByte(t *testing.T) {
	prov := &chatDialHangProvider{}
	// API budget but no FirstByte budget — the API timeout still
	// owns hard cancellation. The select on callCtx.Done() must
	// catch this path too, otherwise we'd park forever even with
	// API set.
	timeouts := LLMCallTimeouts{API: 50 * time.Millisecond}

	start := time.Now()
	result := CallLLMOnce(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		nil, nil,
		timeouts,
		zap.NewNop(),
		"agent-test",
	)
	elapsed := time.Since(start)

	if result.StreamErr == nil {
		t.Fatal("expected API timeout to surface an error when Chat() hangs in dial")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("CallLLMOnce returned after %v; expected <500ms (API timeout should bypass hung Chat)", elapsed)
	}
}
