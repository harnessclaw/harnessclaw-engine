package spawn2

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"harnessclaw-go/internal/agent"
)

// Handle is returned by Spawner.Async. The caller uses it to:
//   - block on Wait until the async spawn completes
//   - Cancel to abort
//   - AgentID for logging / mailbox routing
//
// Internally, Async runs the module in a goroutine; the handle's done
// channel delivers the result.
type Handle interface {
	Wait(ctx context.Context) (*agent.SpawnResult, error)
	Cancel()
	AgentID() string
}

type handleImpl struct {
	agentID string
	cancel  context.CancelFunc
	done    chan struct{}
	result  *agent.SpawnResult
	err     error
}

func (h *handleImpl) AgentID() string { return h.agentID }
func (h *handleImpl) Cancel()         { h.cancel() }

func (h *handleImpl) Wait(ctx context.Context) (*agent.SpawnResult, error) {
	select {
	case <-h.done:
		return h.result, h.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Async runs the spawn in a goroutine and returns a Handle. The
// underlying execution context is derived from ctx; Handle.Cancel
// triggers cancellation of just that derived ctx (caller's ctx is
// unaffected).
func (s *Spawner) Async(ctx context.Context, cfg *agent.SpawnConfig) (Handle, error) {
	if cfg == nil {
		return nil, errors.New("spawn2.Async: nil cfg")
	}
	derived, cancel := context.WithCancel(ctx)

	h := &handleImpl{
		agentID: "async_" + uuid.New().String()[:8],
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	go func() {
		defer close(h.done)
		defer func() {
			if r := recover(); r != nil {
				h.err = errors.New("spawn2.Async: panic in module run")
			}
		}()
		h.result, h.err = s.Sync(derived, cfg)
	}()

	return h, nil
}
