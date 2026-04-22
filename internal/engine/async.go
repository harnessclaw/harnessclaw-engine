package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
)

// ErrNoAgentRegistry is returned when SpawnAsync is called without a registry configured.
var ErrNoAgentRegistry = errors.New("agent registry not configured")

// SpawnAsync implements agent.AsyncSpawner. It launches a sub-agent in a
// background goroutine and returns its agent ID immediately. The result can
// be retrieved from the AgentRegistry once the agent completes.
func (qe *QueryEngine) SpawnAsync(ctx context.Context, cfg *agent.SpawnConfig) (string, error) {
	if qe.agentRegistry == nil {
		return "", ErrNoAgentRegistry
	}

	agentID := agent.NewAsyncAgentID()

	var agentCtx context.Context
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		agentCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	} else {
		agentCtx, cancel = context.WithCancel(ctx)
	}

	asyncAgent := &agent.AsyncAgent{
		ID:        agentID,
		Name:      cfg.Name,
		Status:    agent.AgentStatusRunning,
		Config:    cfg,
		StartedAt: time.Now(),
		Cancel:    cancel,
	}

	qe.agentRegistry.Register(asyncAgent)

	qe.logger.Info("spawning async sub-agent",
		zap.String("agent_id", agentID),
		zap.String("name", cfg.Name),
		zap.String("subagent_type", cfg.SubagentType),
	)

	go func() {
		defer cancel()

		result, err := qe.SpawnSync(agentCtx, cfg)

		qe.agentRegistry.SetResult(agentID, result, err)
		if err != nil {
			qe.agentRegistry.SetStatus(agentID, agent.AgentStatusFailed)
			qe.logger.Error("async sub-agent failed",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		} else {
			qe.agentRegistry.SetStatus(agentID, agent.AgentStatusCompleted)
			qe.logger.Info("async sub-agent completed",
				zap.String("agent_id", agentID),
				zap.Int("turns", result.NumTurns),
			)
		}

		// Notify parent via message broker if available.
		if qe.messageBroker != nil && cfg.Name != "" {
			status := "completed"
			if err != nil {
				status = "failed"
			}
			notif := &agent.WorkerNotification{
				AgentID:    agentID,
				AgentName:  cfg.Name,
				Status:     status,
				DurationMs: time.Since(asyncAgent.StartedAt).Milliseconds(),
			}
			if result != nil {
				notif.Result = result.Output
				// Summary is first 500 chars
				if len(result.Output) > 500 {
					notif.Summary = result.Output[:500]
				} else {
					notif.Summary = result.Output
				}
			}
			content, _ := json.Marshal(notif)
			_ = qe.messageBroker.Send(&agent.AgentMessage{
				From:    cfg.Name,
				To:      cfg.ParentSessionID,
				Type:    agent.MessageTypePlain,
				Content: string(content),
			})
		}
	}()

	return agentID, nil
}

// SetAgentRegistry configures the agent registry for async agent support.
func (qe *QueryEngine) SetAgentRegistry(reg *agent.AgentRegistry) {
	qe.agentRegistry = reg
}

// SetMessageBroker configures the message broker for inter-agent communication.
func (qe *QueryEngine) SetMessageBroker(broker *agent.MessageBroker) {
	qe.messageBroker = broker
}
