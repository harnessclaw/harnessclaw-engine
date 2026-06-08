package handlers

import (
	"context"
	"sync"
	"time"

	"harnessclaw-go/internal/msgbus"
)

// OnResultHandler dispatches KindResult messages to the subscribed
// dispatch.Strategy waiting on each task's resultCh.
// Spec §5.5.3 + §6.2.1.
type OnResultHandler struct {
	mu    sync.Mutex
	subs  map[string]chan msgbus.AgentMessage // taskID → channel
	cache map[string]cachedResult             // taskID → result + ts
}

type cachedResult struct {
	msg msgbus.AgentMessage
	ts  time.Time
}

const resultCacheTTL = 5 * time.Second

func NewOnResult() *OnResultHandler {
	return &OnResultHandler{
		subs:  map[string]chan msgbus.AgentMessage{},
		cache: map[string]cachedResult{},
	}
}

// SubscribeOnce returns a 1-buffer channel for the given task. If a result
// arrived in the last resultCacheTTL, it's delivered immediately.
func (h *OnResultHandler) SubscribeOnce(taskID string) (<-chan msgbus.AgentMessage, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan msgbus.AgentMessage, 1)
	if cached, ok := h.cache[taskID]; ok && time.Since(cached.ts) < resultCacheTTL {
		ch <- cached.msg
		delete(h.cache, taskID)
		return ch, func() {}
	}
	h.subs[taskID] = ch
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, taskID)
		h.mu.Unlock()
	}
}

// Handle is invoked by the bus delivery loop for each KindResult message.
func (h *OnResultHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	res := msg.Payload.(msgbus.ResultMessage)
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[res.TaskID]; ok {
		select {
		case ch <- msg:
		default:
		}
		delete(h.subs, res.TaskID)
		return
	}
	// No subscriber yet — cache briefly
	h.cache[res.TaskID] = cachedResult{msg: msg, ts: time.Now()}
}
