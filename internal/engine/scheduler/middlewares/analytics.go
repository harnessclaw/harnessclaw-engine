package middlewares

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/emit"
)

type Analytics struct {
	Bus emit.Bus
}

func (Analytics) Name() string { return "analytics" }

func (m Analytics) Before(ctx context.Context, p scheduler.SpawnParams, st *scheduler.SpawnState) (context.Context, error) {
	st.Bag["analytics.start"] = time.Now()
	_ = m.Bus.Publish(ctx, emit.Event{
		Topic: scheduler.TopicSpawnStarted,
		Payload: scheduler.SpawnStartedPayload{
			AgentID:      st.AgentID,
			TaskID:       st.TaskID,
			Strategy:     st.Strategy,
			SubagentType: p.Definition.Name, // 用 Definition.Name 作为 observability 标签
			InvokedBy:    p.InvokedBy,
			StartedAt:    time.Now(),
		},
	})
	return ctx, nil
}

func (m Analytics) After(ctx context.Context, _ scheduler.SpawnParams, st *scheduler.SpawnState, r scheduler.Result, err error) {
	start, _ := st.Bag["analytics.start"].(time.Time)
	topic := scheduler.TopicSpawnCompleted
	if err != nil {
		topic = scheduler.TopicSpawnFailed
	}
	var errStr string
	if err != nil {
		errStr = err.Error()
	}
	_ = m.Bus.Publish(ctx, emit.Event{
		Topic: topic,
		Payload: scheduler.SpawnFinishedPayload{
			AgentID:    st.AgentID,
			TaskID:     st.TaskID,
			Strategy:   st.Strategy,
			Status:     r.Status,
			DurationMs: time.Since(start).Milliseconds(),
			Err:        errStr,
		},
	})
}
