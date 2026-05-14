package failover

import (
	"sync"

	"harnessclaw-go/internal/provider"
)

// wrapStream returns a *provider.ChatStream that proxies events from
// `inner` and invokes onSuccess / onFailure exactly once on the first
// call to Err() after the events channel has drained.
//
// Callback semantics:
//   - inner.Err() == nil                                  → onSuccess()
//   - inner.Err() != nil && FailoverWorthy(err) == true   → onFailure(err)
//   - inner.Err() != nil && FailoverWorthy(err) == false  → neither
//     (non-failover-worthy terminal errors like prompt_too_long are
//      the caller's problem, not the failover dispatcher's)
//
// Err() is idempotent: callers (notably the engine's retry layer)
// inspect it more than once on the same stream, but the callbacks
// only fire on the first observation.
//
// Note: the Events channel is NOT proxied — `inner.Events` is exposed
// directly. The stream wrapper only intercepts the terminal Err()
// signal, keeping the goroutine-free contract from bifrost/adapter.go
// intact.
func wrapStream(
	inner *provider.ChatStream,
	onSuccess func(),
	onFailure func(err error),
) *provider.ChatStream {
	var once sync.Once
	return &provider.ChatStream{
		Events: inner.Events,
		Err: func() error {
			err := inner.Err()
			once.Do(func() {
				if err == nil {
					if onSuccess != nil {
						onSuccess()
					}
					return
				}
				if FailoverWorthy(err) && onFailure != nil {
					onFailure(err)
				}
			})
			return err
		},
	}
}
