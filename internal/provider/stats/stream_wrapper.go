package stats

import (
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// onEnd is invoked exactly once when StreamEventMessageEnd lands. usage
// may be nil if the stream errored before MessageEnd.
type onEnd func(usage *types.Usage)

// wrapStream forwards every event from `in` to a fresh channel while
// tapping the MessageEnd usage. The returned ChatStream preserves the
// inner Err() func so retry classifiers stay accurate.
func wrapStream(in *provider.ChatStream, cb onEnd) *provider.ChatStream {
	out := make(chan types.StreamEvent, 32)
	go func() {
		defer close(out)
		var lastUsage *types.Usage
		for ev := range in.Events {
			if ev.Type == types.StreamEventMessageEnd && ev.Usage != nil {
				lastUsage = ev.Usage
			}
			out <- ev
		}
		cb(lastUsage)
	}()
	return &provider.ChatStream{Events: out, Err: in.Err}
}
