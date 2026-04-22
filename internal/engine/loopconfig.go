package engine

import (
	"harnessclaw-go/pkg/types"
)

// newDrainChannel creates a buffered channel and a goroutine that continuously
// drains it, preventing sub-agent event writes from blocking. The caller must
// close the returned channel to stop the drain goroutine.
func newDrainChannel() chan types.EngineEvent {
	ch := make(chan types.EngineEvent, 64)
	go func() {
		for range ch {
			// discard all events
		}
	}()
	return ch
}
