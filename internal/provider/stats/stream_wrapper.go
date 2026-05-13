package stats

import (
	"context"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// onEnd is invoked exactly once when the stream is fully drained. usage
// may be nil if the stream errored before MessageEnd or if ctx was
// cancelled before any usage arrived. model is the LLM model that
// actually served the call, captured from MessageEnd.Model — empty
// when the provider didn't report one.
type onEnd func(usage *types.Usage, model string)

// wrapStream forwards every event from `in` to a fresh channel while
// tapping MessageEnd usage and model. The returned ChatStream preserves
// the inner Err() func so retry classifiers stay accurate.
//
// ctx governs the wrapper goroutine's lifetime: if the consumer breaks
// out of the event loop early (or the caller cancels), ctx cancellation
// allows the goroutine to exit cleanly rather than blocking on a full
// output buffer forever. The callback still fires (with whatever usage
// was captured by then) so the LLMCalls counter reflects reality.
func wrapStream(ctx context.Context, in *provider.ChatStream, cb onEnd) *provider.ChatStream {
	out := make(chan types.StreamEvent, 32)
	go func() {
		defer close(out)
		var lastUsage *types.Usage
		var lastModel string
		for {
			select {
			case ev, ok := <-in.Events:
				if !ok {
					cb(lastUsage, lastModel)
					return
				}
				if ev.Type == types.StreamEventMessageEnd {
					if ev.Usage != nil {
						lastUsage = ev.Usage
					}
					if ev.Model != "" {
						lastModel = ev.Model
					}
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					cb(lastUsage, lastModel)
					return
				}
			case <-ctx.Done():
				cb(lastUsage, lastModel)
				return
			}
		}
	}()
	return &provider.ChatStream{Events: out, Err: in.Err}
}
