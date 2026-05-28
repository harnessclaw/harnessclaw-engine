package spawn

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
func (s *Spawner) SpawnAsync(ctx context.Context, cfg *agent.SpawnConfig) (string, error) {
	reg := s.deps.AgentRegistry()
	if reg == nil {
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

	reg.Register(asyncAgent)

	logger := s.deps.Logger()
	logger.Info("spawning async sub-agent",
		zap.String("agent_id", agentID),
		zap.String("name", cfg.Name),
		zap.String("subagent_type", cfg.SubagentType),
	)

	go func() {
		defer cancel()

		result, err := s.SpawnSync(agentCtx, cfg)

		reg.SetResult(agentID, result, err)
		if err != nil {
			reg.SetStatus(agentID, agent.AgentStatusFailed)
			logger.Error("async sub-agent failed",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		} else {
			reg.SetStatus(agentID, agent.AgentStatusCompleted)
			logger.Info("async sub-agent completed",
				zap.String("agent_id", agentID),
				zap.Int("turns", result.NumTurns),
			)
		}

		// Notify parent via message broker if available.
		if broker := s.deps.MessageBroker(); broker != nil && cfg.Name != "" {
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
			_ = broker.Send(&agent.AgentMessage{
				From:    cfg.Name,
				To:      cfg.ParentSessionID,
				Type:    agent.MessageTypePlain,
				Content: string(content),
			})
		}
	}()

	return agentID, nil
}
