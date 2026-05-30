package mention

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/pkg/types"
)

// Router detects @agent_name at the start of a user message and, if
// the named agent is registered, spawns it directly (bypassing the
// host engine's main loop).
type Router struct {
	spawner *spawn.Spawner
	defReg  *agent.AgentDefinitionRegistry
	parser  *agent.MentionParser
	logger  *zap.Logger
}

// NewRouter constructs a Router. parser may be created from defReg via
// agent.NewMentionParser; passed in to allow reuse / testing.
func NewRouter(spawner *spawn.Spawner, defReg *agent.AgentDefinitionRegistry, parser *agent.MentionParser) *Router {
	return &Router{
		spawner: spawner,
		defReg:  defReg,
		parser:  parser,
		logger:  zap.NewNop(),
	}
}

// TryRoute inspects msg's text. If it starts with @agent_name matching
// a registered AgentDefinition, returns a channel where the spawn's
// events stream and the dispatch result terminates. Returns nil when
// no mention is detected — caller falls through to its main path.
//
// The goroutine appends msg to sess before spawning, so callers MUST
// NOT call sess.AddMessage(*msg) themselves when TryRoute returns a
// non-nil channel.
//
// Caller is responsible for closing/draining the channel.
func (r *Router) TryRoute(ctx context.Context, sess *session.Session, msg *types.Message) <-chan types.EngineEvent {
	text := extractText(msg)
	match := r.parser.Parse(text)
	if !match.Matched {
		return nil
	}
	def := r.defReg.Get(match.AgentName)
	if def == nil {
		return nil
	}

	out := make(chan types.EngineEvent, 64)
	go func() {
		defer close(out)
		sess.AddMessage(*msg)

		out <- types.EngineEvent{
			Type:      types.EngineEventAgentRouted,
			AgentName: def.Name,
			AgentDesc: def.Description,
		}

		// Prepend the agent definition's SystemPrompt to the
		// user-supplied prompt when present, matching the legacy
		// emma.ProcessWithAgent behavior.
		promptText := match.Message
		if def.SystemPrompt != "" {
			promptText = def.SystemPrompt + "\n\n" + match.Message
		}

		if def.AutoTeam && len(def.SubAgents) > 0 {
			r.logger.Info("team workflow not yet implemented, falling back to single agent",
				zap.String("agent", def.Name),
			)
		}

		// SubagentType MUST be def.Name (the registry key), not
		// def.Profile (a prompt-profile selector).
		cfg := &agent.SpawnConfig{
			Prompt:          promptText,
			SubagentType:    def.Name,
			Name:            def.Name,
			Description:     def.Description,
			AgentType:       def.AgentType,
			Model:           def.Model,
			MaxTurns:        def.MaxTurns,
			ParentSessionID: sess.ID,
			ParentAgentID:   "main",
			ParentOut:       out,
		}

		result, err := r.spawner.Sync(ctx, cfg)
		if err != nil {
			out <- types.EngineEvent{Type: types.EngineEventError, Error: err}
			out <- types.EngineEvent{
				Type: types.EngineEventDone,
				Terminal: &types.Terminal{
					Reason:  types.TerminalModelError,
					Message: err.Error(),
				},
				Usage: &types.Usage{},
			}
			return
		}
		out <- types.EngineEvent{
			Type:     types.EngineEventDone,
			Terminal: result.Terminal,
			Usage:    result.Usage,
		}
	}()
	return out
}

func extractText(msg *types.Message) string {
	if msg == nil {
		return ""
	}
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeText {
			return b.Text
		}
	}
	return ""
}
